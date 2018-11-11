package main

import (
	"bufio"
	"bytes"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"mime"
	"net/http"
	"os"
	"path/filepath"
	"strings"

	"github.com/aws/aws-sdk-go/aws"
	"github.com/aws/aws-sdk-go/aws/credentials"
	"github.com/aws/aws-sdk-go/aws/session"
	"github.com/aws/aws-sdk-go/service/dynamodb"
	"github.com/aws/aws-sdk-go/service/dynamodb/dynamodbattribute"
	"github.com/julienschmidt/httprouter"
	"github.com/jung-kurt/gofpdf"
	"github.com/rs/xid"
	hashids "github.com/speps/go-hashids"
)

var funcMap = template.FuncMap{
	"CompletePrice": CompletePrice,
	"Secret":        Secret,
}

var overview, _ = template.ParseFiles("public/html/index.html")
var admin, _ = template.New("admin.html").Funcs(funcMap).ParseFiles("public/html/admin.html")
var orderTemplate, _ = template.ParseFiles("public/html/order.html")
var myOrderTemplate, _ = template.ParseFiles("public/html/myorder.html")

var sess, err = session.NewSession(&aws.Config{
	Region:      aws.String("eu-central-1"),
	Credentials: credentials.NewSharedCredentials("credentials", "dynamodb"),
})
var svc = dynamodb.New(sess)

var hd = hashids.NewData()
var hashGen *hashids.HashID

type config struct {
	Username string
	Password string
	Phone    string
	Secret   string
}

var c config

type order struct {
	Id      string
	Name    string
	Items   []orderItem
	IsPayed bool
}

type address struct {
	Id     string
	Name   string
	Number string
}

func (o order) CalcPriceValue() int {
	price := 0

	for _, item := range o.Items {
		price += item.Size.Price

		for _, extra := range item.Extras {
			price += extraPrice(item.Size, extra)
		}
	}

	return price
}

func (o order) CalcPrice() string {
	return FormatPrice(o.CalcPriceValue())
}

func FormatPrice(price int) string {
	return fmt.Sprintf("%.2f", float64(price)/100.0)
}

func CompletePrice() string {
	price := 0
	orders := getOrders()

	for _, o := range orders {
		price += o.CalcPriceValue()
	}

	return FormatPrice(price)
}

func Secret() string {
	return c.Secret
}

func (o order) OrderItems() string {
	names := []string{}

	for _, item := range o.Items {
		extraText := ""
		for _, extra := range item.Extras {
			extraText += ", " + extra.Name
		}

		names = append(names, item.Name+"("+item.Size.Name+extraText+")")
	}

	return strings.Join(names, ", ")
}

func (o order) ShowIsPayed() string {
	if o.IsPayed {
		return "Ja"
	}
	return "Nein"
}

type menuItem struct {
	Name        string
	Description string
	Sizes       []size
	Extras      []extra
}

type orderItem struct {
	Name   string
	Size   size
	Extras []extra
}

type size struct {
	Name  string
	Price int
}

type extra struct {
	Name  string
	Price int
}

type changePayed struct {
	Id      string
	IsPayed bool
}

type deleteOrder struct {
	Id string
}

type gzipResponseWriter struct {
	io.Writer
	http.ResponseWriter
}

func (w gzipResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

//TODO yes ugly hack, but I did not want to rewrite the entire extra code
func extraPrice(s size, e extra) int {
	if s.Name == "klein, 24 cm" {
		return e.Price
	} else if s.Name == "groß, 32 cm" {
		return e.Price + 10
	} else if s.Name == "Family, 45x32 cm" {
		return e.Price + 60
	} else {
		return e.Price + 130
	}
}

func ServeStatic(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	path := ps.ByName("filepath")
	body, _ := ioutil.ReadFile("public/" + path)
	w.Header().Set("Content-Type", mime.TypeByExtension(filepath.Ext(path)))

	w.Write(body)
}

func Index(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "text/html")

	orders := getOrders()
	overview.Execute(w, orders)
}

func MyOrder(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "text/html")

	id := ps.ByName("order")

	params := &dynamodb.GetItemInput{
		TableName: aws.String("Orders"),
		Key: map[string]*dynamodb.AttributeValue{
			"Id": {
				S: aws.String(id),
			},
		},
	}

	resp, err := svc.GetItem(params)
	if err != nil {
		fmt.Printf("ERROR: %v\n", err.Error())
		return
	}

	var order order
	err = dynamodbattribute.UnmarshalMap(resp.Item, &order)

	myOrderTemplate.Execute(w, order)
}

func Admin(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "text/html")
	orders := getOrders()
	admin.Execute(w, orders)
}

func Order(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "text/html")
	orders := getOrders()
	orderTemplate.Execute(w, orders)
}

func ChangePayed(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")

	decoder := json.NewDecoder(r.Body)
	var t changePayed
	err := decoder.Decode(&t)
	if err != nil {
		panic(err)
	}

	defer r.Body.Close()

	params := &dynamodb.UpdateItemInput{
		TableName: aws.String("Orders"),
		Key: map[string]*dynamodb.AttributeValue{
			"Id": {
				S: aws.String(t.Id),
			},
		},
		UpdateExpression: aws.String("set IsPayed = :IsPayed"),
		ExpressionAttributeValues: map[string]*dynamodb.AttributeValue{
			":IsPayed": {
				BOOL: aws.Bool(t.IsPayed),
			},
		},
	}

	// update the item
	_, err = svc.UpdateItem(params)
	if err != nil {
		fmt.Printf("ERROR: %v\n", err.Error())
		return
	}
}

func DeleteOrder(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")

	decoder := json.NewDecoder(r.Body)
	var t deleteOrder
	err := decoder.Decode(&t)
	if err != nil {
		panic(err)
	}

	defer r.Body.Close()

	// create the api params
	params := &dynamodb.DeleteItemInput{
		TableName: aws.String("Orders"),
		Key: map[string]*dynamodb.AttributeValue{
			"Id": {
				S: aws.String(t.Id),
			},
		},
	}

	// delete the item
	_, err = svc.DeleteItem(params)
	if err != nil {
		fmt.Printf("ERROR: %v\n", err.Error())
		return
	}
}

func SendOrder(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")

	decoder := json.NewDecoder(r.Body)
	var t order
	err := decoder.Decode(&t)
	if err != nil {
		panic(err)
	}

	t.IsPayed = false

	t.Id = xid.New().String()
	orderAVMap, err := dynamodbattribute.MarshalMap(t)
	if err != nil {
		panic("Cannot marshal order into AttributeValue map")
	}

	// create the api params
	params := &dynamodb.PutItemInput{
		TableName: aws.String("Orders"),
		Item:      orderAVMap,
	}

	// put the item
	resp, err := svc.PutItem(params)
	if err != nil {
		fmt.Printf("ERROR: %v\n", err.Error())
		return
	}
	fmt.Println(resp)

	defer r.Body.Close()

	json.NewEncoder(w).Encode(t)
}

//multiply price named mp for convenience
func mp(sizes []size, factor float64) []size {
	for _, size := range sizes {
		size.Price = int(float64(size.Price) * factor)
	}
	return sizes
}

func Items(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")

	extrasAll := []extra{extra{Name: "Basilikum", Price: 50}, extra{Name: "Knoblauch", Price: 50}, extra{Name: "Oregano", Price: 50}, extra{Name: "Ananas", Price: 50}, extra{Name: "Artischockenherzen", Price: 50}, extra{Name: "Bacon", Price: 50}, extra{Name: "Barbecuesauce", Price: 50}, extra{Name: "Blattspinat", Price: 50}, extra{Name: "Brokkoli", Price: 50}, extra{Name: "Champignons", Price: 50}, extra{Name: "Eier", Price: 50}, extra{Name: "Fetakäse", Price: 50}, extra{Name: "Frutti di Mare", Price: 50}, extra{Name: "Gorgonzola", Price: 50}, extra{Name: "Gouda", Price: 50}, extra{Name: "Hühnerbrust", Price: 50}, extra{Name: "Kapern", Price: 50}, extra{Name: "Mais", Price: 50}, extra{Name: "Mozzarella", Price: 50}, extra{Name: "Olivenscheiben", Price: 50}, extra{Name: "Paprika", Price: 50}, extra{Name: "Peperoniringe", Price: 50}, extra{Name: "Peperoniwurst", Price: 50}, extra{Name: "Remoulade", Price: 50}, extra{Name: "Salami", Price: 50}, extra{Name: "Salsa", Price: 50}, extra{Name: "Sardellen", Price: 50}, extra{Name: "Sauce Bolognese", Price: 50}, extra{Name: "Shrimps", Price: 50}, extra{Name: "Spargel", Price: 50}, extra{Name: "Tabasco", Price: 50}, extra{Name: "Taco Beef", Price: 50}, extra{Name: "Thunfisch", Price: 50}, extra{Name: "Zwiebelringe", Price: 50}, extra{Name: "frische Champignons", Price: 50}, extra{Name: "frische Tomatenscheiben", Price: 50}, extra{Name: "italienischer Vorderschinken", Price: 50}, extra{Name: "mexikanische Jalapenos", Price: 50}}

	priceFactor := 1.1

	size1 := []size{size{Name: "klein, 24 cm", Price: 650}, size{Name: "groß, 32 cm", Price: 800}, size{Name: "Family, 45x32 cm", Price: 1420}, size{Name: "Party, 60x40 cm", Price: 1690}}
	pizza1 := menuItem{Name: "Pizza Margherita", Description: "mit Pizzasauce, Mozzarella und Basilikum", Sizes: mp(size1, priceFactor), Extras: extrasAll}

	size2 := []size{size{Name: "klein, 24 cm", Price: 700}, size{Name: "groß, 32 cm", Price: 860}, size{Name: "Family, 45x32 cm", Price: 1530}, size{Name: "Party, 60x40 cm", Price: 1870}}
	pizza2 := menuItem{Name: "Pizza New York", Description: "mit Hühnerbrust und Oliven", Sizes: mp(size2, priceFactor), Extras: extrasAll}

	size3 := []size{size{Name: "klein, 24 cm", Price: 700}, size{Name: "groß, 32 cm", Price: 860}, size{Name: "Family, 45x32 cm", Price: 1530}, size{Name: "Party, 60x40 cm", Price: 1870}}
	pizza3 := menuItem{Name: "Pizza Popeye", Description: "mit Spinat und Feta", Sizes: mp(size3, priceFactor), Extras: extrasAll}

	size4 := []size{size{Name: "klein, 24 cm", Price: 700}, size{Name: "groß, 32 cm", Price: 860}, size{Name: "Family, 45x32 cm", Price: 1530}, size{Name: "Party, 60x40 cm", Price: 1870}}
	pizza4 := menuItem{Name: "Pizza Hawaii", Description: "mit Schinken und Ananas", Sizes: mp(size4, priceFactor), Extras: extrasAll}

	size5 := []size{size{Name: "klein, 24 cm", Price: 750}, size{Name: "groß, 32 cm", Price: 920}, size{Name: "Family, 45x32 cm", Price: 1640}, size{Name: "Party, 60x40 cm", Price: 2050}}
	pizza5 := menuItem{Name: "Pizza Mary", Description: "mit Schinken, Salami und Champignons", Sizes: mp(size5, priceFactor), Extras: extrasAll}

	size6 := []size{size{Name: "klein, 24 cm", Price: 750}, size{Name: "groß, 32 cm", Price: 920}, size{Name: "Family, 45x32 cm", Price: 1640}, size{Name: "Party, 60x40 cm", Price: 2050}}
	pizza6 := menuItem{Name: "Pizza Samoa", Description: "mit Thunfisch, Zwiebelringen und Ananas", Sizes: mp(size6, priceFactor), Extras: extrasAll}

	size7 := []size{size{Name: "klein, 24 cm", Price: 750}, size{Name: "groß, 32 cm", Price: 920}, size{Name: "Family, 45x32 cm", Price: 1640}, size{Name: "Party, 60x40 cm", Price: 2050}}
	pizza7 := menuItem{Name: "Pizza Texas", Description: "mit Taco Beef, Jalapenos und Bohnen", Sizes: mp(size7, priceFactor), Extras: extrasAll}

	size8 := []size{size{Name: "klein, 24 cm", Price: 800}, size{Name: "groß, 32 cm", Price: 980}, size{Name: "Family, 45x32 cm", Price: 1750}, size{Name: "Party, 60x40 cm", Price: 2230}}
	pizza8 := menuItem{Name: "Pizza Jazz", Description: "mit Schinken, Spargel, Tomaten und Barbecuesauce", Sizes: mp(size8, priceFactor), Extras: extrasAll}

	size9 := []size{size{Name: "klein, 24 cm", Price: 800}, size{Name: "groß, 32 cm", Price: 980}, size{Name: "Family, 45x32 cm", Price: 1750}, size{Name: "Party, 60x40 cm", Price: 2230}}
	pizza9 := menuItem{Name: "Pizza Veggie", Description: "mit Broccoli, Tomaten, Paprika und Artischocken", Sizes: mp(size9, priceFactor), Extras: extrasAll}

	size10 := []size{size{Name: "klein, 24 cm", Price: 850}, size{Name: "groß, 32 cm", Price: 1040}, size{Name: "Family, 45x32 cm", Price: 1860}, size{Name: "Party, 60x40 cm", Price: 2410}}
	pizza10 := menuItem{Name: "Pizza Capricciosa", Description: "mit Schinken, Salami, Oliven, Paprika und Zwiebeln", Sizes: mp(size10, priceFactor), Extras: extrasAll}

	size11 := []size{size{Name: "klein, 24 cm", Price: 850}, size{Name: "groß, 32 cm", Price: 1040}, size{Name: "Family, 45x32 cm", Price: 1860}, size{Name: "Party, 60x40 cm", Price: 2410}}
	pizza11 := menuItem{Name: "Pizza Mexicana", Description: "mit Peperoniwurst, Speck, Taco Beef, Jalapenos und Zwiebeln", Sizes: mp(size11, priceFactor), Extras: extrasAll}

	size12 := []size{size{Name: "klein, 24 cm", Price: 850}, size{Name: "groß, 32 cm", Price: 1040}, size{Name: "Family, 45x32 cm", Price: 1860}, size{Name: "Party, 60x40 cm", Price: 2410}}
	pizza12 := menuItem{Name: "Pizza Outback", Description: "mit Taco Beef, Schinken, Zwiebeln, Jalapenos und Ei", Sizes: mp(size12, priceFactor), Extras: extrasAll}

	size13 := []size{size{Name: "klein, 24 cm", Price: 850}, size{Name: "groß, 32 cm", Price: 1040}, size{Name: "Family, 45x32 cm", Price: 1860}, size{Name: "Party, 60x40 cm", Price: 2410}}
	pizza13 := menuItem{Name: "Pizza Beverly Hills", Description: "mit Hühnerbrust, Taco Beef, Broccoli, Ananas und Barbecuesauce", Sizes: mp(size13, priceFactor), Extras: extrasAll}

	size14 := []size{size{Name: "klein, 24 cm", Price: 850}, size{Name: "groß, 32 cm", Price: 1040}, size{Name: "Family, 45x32 cm", Price: 1860}, size{Name: "Party, 60x40 cm", Price: 2410}}
	pizza14 := menuItem{Name: "Pizza Speciale", Description: "mit Schinken, Salami, Champignons, Paprika und Ei", Sizes: mp(size14, priceFactor), Extras: extrasAll}

	size15 := []size{size{Name: "klein, 24 cm", Price: 900}, size{Name: "groß, 32 cm", Price: 1100}, size{Name: "Family, 45x32 cm", Price: 1970}, size{Name: "Party, 60x40 cm", Price: 2590}}
	pizza15 := menuItem{Name: "Pizza Full House", Description: "mit Schinken, Peperoniwurst, Speck, Paprika, Peperoni und Ei", Sizes: mp(size15, priceFactor), Extras: extrasAll}

	size16 := []size{size{Name: "klein, 24 cm", Price: 600}, size{Name: "groß, 32 cm", Price: 740}, size{Name: "Family, 45x32 cm", Price: 1310}, size{Name: "Party, 60x40 cm", Price: 1510}}
	pizza16 := menuItem{Name: "Pizza Basic/Wunschpizza", Description: "mit Pizzasauce und Käse", Sizes: mp(size16, priceFactor), Extras: extrasAll}

	json.NewEncoder(w).Encode([]menuItem{pizza1, pizza2, pizza3, pizza4, pizza5, pizza6, pizza7, pizza8, pizza9, pizza10, pizza11, pizza12, pizza13, pizza14, pizza15, pizza16})
}

func getOrders() []order {
	// create the api params
	params := &dynamodb.ScanInput{
		TableName: aws.String("Orders"),
	}

	orders := make([]order, 0)

	err := svc.ScanPages(params, func(page *dynamodb.ScanOutput, lastPage bool) bool {
		var orderPage []order
		err := dynamodbattribute.UnmarshalListOfMaps(page.Items, &orderPage)
		if err != nil {
			// print the error and continue receiving pages
			fmt.Printf("\nCould not unmarshal AWS data: err = %v\n", err)
			return true
		}

		orders = append(orders, orderPage...)

		return true
	})
	if err != nil {
		fmt.Printf("ERROR: %v\n", err.Error())
	}

	return orders
}

func Pdf(addressVal address) string {
	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetAuthor("Christoph Müller", false)
	pdf.AddPage()
	pdf.SetFont("Times", "", 16)

	tr := pdf.UnicodeTranslatorFromDescriptor("")

	message := "Treffpunkt 8 (blaues Schild) / Parkplatz Informatik, bitte anrufen wenn da, wir kommen dann raus"
	addressTxt := fmt.Sprintf("%s\nTheodor-Boveri-Weg\n97074 Würzburg\n%s\n\nBemerkung:\n%s\n\nZahlung: Bar\n\n", addressVal.Name, addressVal.Number, message)

	pdf.MultiCell(0, 8, tr(addressTxt), "", "", false)

	// if only go would be functional ...
	for _, o := range getOrders() {
		for _, item := range o.Items {
			pizzaName := item.Name + " (" + item.Size.Name + ") "
			pdf.SetFont("Times", "B", 16)
			pdf.MultiCell(180, 10, tr(pizzaName), "1", "", false)
			pdf.SetFont("Times", "", 16)
			extraTxt := ""
			for _, extra := range item.Extras {
				extraTxt += "+" + extra.Name + " "
			}
			if extraTxt != "" {
				pdf.MultiCell(180, 10, tr(extraTxt), "1", "", false)
			}
			pdf.Ln(5)
		}
	}

	var b bytes.Buffer
	writer := bufio.NewWriter(&b)
	pdf.Output(writer)
	return base64.StdEncoding.EncodeToString(b.Bytes())
}

func FaxOrder(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")

	decoder := json.NewDecoder(r.Body)
	var address address
	err := decoder.Decode(&address)
	if err != nil || strings.TrimSpace(address.Name) == "" || strings.TrimSpace(address.Number) == "" {
		json.NewEncoder(w).Encode(false)
		return
	}

	client := &http.Client{}

	faxJson, err := json.Marshal(struct {
		FaxlineID     string `json:"faxlineId"`
		Recipient     string `json:"recipient"`
		Filename      string `json:"filename"`
		Base64Content string `json:"base64Content"`
	}{"f0", c.Phone, "bestellung.pdf", Pdf(address)})

	if err != nil {
		json.NewEncoder(w).Encode(false)
		return
	}

	req, err := http.NewRequest("POST", "https://api.sipgate.com/v2/sessions/fax", bytes.NewBuffer(faxJson))

	if err != nil {
		json.NewEncoder(w).Encode(false)
		return
	}

	req.SetBasicAuth(c.Username, c.Password)
	req.Header.Add("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		json.NewEncoder(w).Encode(false)
		return
	}

	if resp.StatusCode != 200 {
		println("Error while sending the Fax: " + string(resp.StatusCode))
		json.NewEncoder(w).Encode(false)
		return
	}

	bodyText, err := ioutil.ReadAll(resp.Body)
	println(string(bodyText))

	json.NewEncoder(w).Encode(true)
}

func main() {
	port := os.Getenv("PORT")
	if port == "" {
		port = "80"
	}

	//stage := os.Getenv("UP_STAGE")

	hashGen, _ = hashids.NewWithData(hd)

	raw, err := ioutil.ReadFile("./config.json")
	if err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}

	err = json.Unmarshal(raw, &c)

	if err != nil {
		fmt.Println(err.Error())
		os.Exit(1)
	}

	key := c.Secret

	router := httprouter.New()
	router.GET("/", Index)
	router.GET("/admin"+key, Admin)
	router.GET("/items", Items)
	router.POST("/admin"+key, ChangePayed)
	router.POST("/deleteOrder"+key, DeleteOrder)
	router.GET("/order", Order)
	router.GET("/myorder/:order", MyOrder)
	router.POST("/order", SendOrder)
	router.GET("/orders", Index)
	router.POST("/faxorder"+key, FaxOrder)
	router.GET("/public/*filepath", ServeStatic)

	log.Fatal(http.ListenAndServe(":"+os.Getenv("PORT"), router))
}
