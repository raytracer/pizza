package main

import (
	"compress/gzip"
	"encoding/json"
	"fmt"
	"html/template"
	"io"
	"io/ioutil"
	"log"
	"mime"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/julienschmidt/httprouter"
	"github.com/jung-kurt/gofpdf"
)

var funcMap = template.FuncMap{
	"CompletePrice": CompletePrice,
	"Secret":        Secret,
}

var overview, _ = template.ParseFiles("public/html/index.html")
var admin, _ = template.New("admin.html").Funcs(funcMap).ParseFiles("public/html/admin.html")
var orderTemplate, _ = template.ParseFiles("public/html/order.html")
var myOrderTemplate, _ = template.ParseFiles("public/html/myorder.html")

var mutex = &sync.Mutex{}
var orders = []order{}
var orderNr = 1
var addressVal = address{Name: "", Number: ""}

type config struct {
	Username string
	Password string
	Phone    string
	Secret   string
}

var c config

type order struct {
	Id      int
	Name    string
	Items   []orderItem
	IsPayed bool
}

type address struct {
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
	Id      int
	IsPayed bool
}

type deleteOrder struct {
	Id int
}

type gzipResponseWriter struct {
	io.Writer
	http.ResponseWriter
}

func (w gzipResponseWriter) Write(b []byte) (int, error) {
	return w.Writer.Write(b)
}

func makeGzipHandler(fn httprouter.Handle) httprouter.Handle {
	return func(w http.ResponseWriter, r *http.Request, params httprouter.Params) {
		if !strings.Contains(r.Header.Get("Accept-Encoding"), "gzip") {
			fn(w, r, params)
			return
		}
		w.Header().Set("Content-Encoding", "gzip")
		gz := gzip.NewWriter(w)
		defer gz.Close()
		gzr := gzipResponseWriter{Writer: gz, ResponseWriter: w}
		fn(gzr, r, params)
	}
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

	overview.Execute(w, orders)
}

func MyOrder(w http.ResponseWriter, r *http.Request, ps httprouter.Params) {
	w.Header().Set("Content-Type", "text/html")

	i, err := strconv.Atoi(ps.ByName("order"))

	if err == nil && i >= 0 {
		for _, order := range orders {
			if order.Id == i {
				myOrderTemplate.Execute(w, order)
			}
		}
	}
}

func Admin(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "text/html")
	admin.Execute(w, orders)
}

func Order(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "text/html")
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

	for i := 0; i < len(orders); i++ {
		if orders[i].Id == t.Id {
			orders[i].IsPayed = t.IsPayed
		}
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

	toBeDeleted := -1

	for i := 0; i < len(orders); i++ {
		if orders[i].Id == t.Id {
			toBeDeleted = i
		}
	}

	if toBeDeleted > -1 {
		orders = append(orders[:toBeDeleted], orders[toBeDeleted+1:]...)
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
	mutex.Lock()

	t.Id = orderNr
	orderNr++
	orders = append(orders, t)

	mutex.Unlock()

	defer r.Body.Close()

	json.NewEncoder(w).Encode(t)
}

func Items(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")

	extrasAll := []extra{extra{Name: "Basilikum", Price: 50}, extra{Name: "Knoblauch", Price: 50}, extra{Name: "Oregano", Price: 50}, extra{Name: "Ananas", Price: 50}, extra{Name: "Artischockenherzen", Price: 50}, extra{Name: "Bacon", Price: 50}, extra{Name: "Barbecuesauce", Price: 50}, extra{Name: "Blattspinat", Price: 50}, extra{Name: "Brokkoli", Price: 50}, extra{Name: "Champignons", Price: 50}, extra{Name: "Eier", Price: 50}, extra{Name: "Fetakäse", Price: 50}, extra{Name: "Frutti di Mare", Price: 50}, extra{Name: "Gorgonzola", Price: 50}, extra{Name: "Gouda", Price: 50}, extra{Name: "Hühnerbrust", Price: 50}, extra{Name: "Kapern", Price: 50}, extra{Name: "Mais", Price: 50}, extra{Name: "Mozzarella", Price: 50}, extra{Name: "Olivenscheiben", Price: 50}, extra{Name: "Paprika", Price: 50}, extra{Name: "Peperoniringe", Price: 50}, extra{Name: "Peperoniwurst", Price: 50}, extra{Name: "Remoulade", Price: 50}, extra{Name: "Salami", Price: 50}, extra{Name: "Salsa", Price: 50}, extra{Name: "Sardellen", Price: 50}, extra{Name: "Sauce Bolognese", Price: 50}, extra{Name: "Shrimps", Price: 50}, extra{Name: "Spargel", Price: 50}, extra{Name: "Tabasco", Price: 50}, extra{Name: "Taco Beef", Price: 50}, extra{Name: "Thunfisch", Price: 50}, extra{Name: "Zwiebelringe", Price: 50}, extra{Name: "frische Champignons", Price: 50}, extra{Name: "frische Tomatenscheiben", Price: 50}, extra{Name: "italienischer Vorderschinken", Price: 50}, extra{Name: "mexikanische Jalapenos", Price: 50}}

	size1 := []size{size{Name: "klein, 24 cm", Price: 650}, size{Name: "groß, 32 cm", Price: 800}, size{Name: "Family, 45x32 cm", Price: 1420}, size{Name: "Party, 60x40 cm", Price: 1690}}
	pizza1 := menuItem{Name: "Pizza Margherita", Description: "mit Pizzasauce, Mozzarella und Basilikum", Sizes: size1, Extras: extrasAll}

	size2 := []size{size{Name: "klein, 24 cm", Price: 700}, size{Name: "groß, 32 cm", Price: 860}, size{Name: "Family, 45x32 cm", Price: 1530}, size{Name: "Party, 60x40 cm", Price: 1870}}
	pizza2 := menuItem{Name: "Pizza New York", Description: "mit Hühnerbrust und Oliven", Sizes: size2, Extras: extrasAll}

	size3 := []size{size{Name: "klein, 24 cm", Price: 700}, size{Name: "groß, 32 cm", Price: 860}, size{Name: "Family, 45x32 cm", Price: 1530}, size{Name: "Party, 60x40 cm", Price: 1870}}
	pizza3 := menuItem{Name: "Pizza Popeye", Description: "mit Spinat und Feta", Sizes: size3, Extras: extrasAll}

	size4 := []size{size{Name: "klein, 24 cm", Price: 700}, size{Name: "groß, 32 cm", Price: 860}, size{Name: "Family, 45x32 cm", Price: 1530}, size{Name: "Party, 60x40 cm", Price: 1870}}
	pizza4 := menuItem{Name: "Pizza Hawaii", Description: "mit Schinken und Ananas", Sizes: size4, Extras: extrasAll}

	size5 := []size{size{Name: "klein, 24 cm", Price: 750}, size{Name: "groß, 32 cm", Price: 920}, size{Name: "Family, 45x32 cm", Price: 1640}, size{Name: "Party, 60x40 cm", Price: 2050}}
	pizza5 := menuItem{Name: "Pizza Mary", Description: "mit Schinken, Salami und Champignons", Sizes: size5, Extras: extrasAll}

	size6 := []size{size{Name: "klein, 24 cm", Price: 750}, size{Name: "groß, 32 cm", Price: 920}, size{Name: "Family, 45x32 cm", Price: 1640}, size{Name: "Party, 60x40 cm", Price: 2050}}
	pizza6 := menuItem{Name: "Pizza Samoa", Description: "mit Thunfisch, Zwiebelringen und Ananas", Sizes: size6, Extras: extrasAll}

	size7 := []size{size{Name: "klein, 24 cm", Price: 750}, size{Name: "groß, 32 cm", Price: 920}, size{Name: "Family, 45x32 cm", Price: 1640}, size{Name: "Party, 60x40 cm", Price: 2050}}
	pizza7 := menuItem{Name: "Pizza Texas", Description: "mit Taco Beef, Jalapenos und Bohnen", Sizes: size7, Extras: extrasAll}

	size8 := []size{size{Name: "klein, 24 cm", Price: 800}, size{Name: "groß, 32 cm", Price: 980}, size{Name: "Family, 45x32 cm", Price: 1750}, size{Name: "Party, 60x40 cm", Price: 2230}}
	pizza8 := menuItem{Name: "Pizza Jazz", Description: "mit Schinken, Spargel, Tomaten und Barbecuesauce", Sizes: size8, Extras: extrasAll}

	size9 := []size{size{Name: "klein, 24 cm", Price: 800}, size{Name: "groß, 32 cm", Price: 980}, size{Name: "Family, 45x32 cm", Price: 1750}, size{Name: "Party, 60x40 cm", Price: 2230}}
	pizza9 := menuItem{Name: "Pizza Veggie", Description: "mit Broccoli, Tomaten, Paprika und Artischocken", Sizes: size9, Extras: extrasAll}

	size10 := []size{size{Name: "klein, 24 cm", Price: 850}, size{Name: "groß, 32 cm", Price: 1040}, size{Name: "Family, 45x32 cm", Price: 1860}, size{Name: "Party, 60x40 cm", Price: 2410}}
	pizza10 := menuItem{Name: "Pizza Capricciosa", Description: "mit Schinken, Salami, Oliven, Paprika und Zwiebeln", Sizes: size10, Extras: extrasAll}

	size11 := []size{size{Name: "klein, 24 cm", Price: 850}, size{Name: "groß, 32 cm", Price: 1040}, size{Name: "Family, 45x32 cm", Price: 1860}, size{Name: "Party, 60x40 cm", Price: 2410}}
	pizza11 := menuItem{Name: "Pizza Mexicana", Description: "mit Peperoniwurst, Speck, Taco Beef, Jalapenos und Zwiebeln", Sizes: size11, Extras: extrasAll}

	size12 := []size{size{Name: "klein, 24 cm", Price: 850}, size{Name: "groß, 32 cm", Price: 1040}, size{Name: "Family, 45x32 cm", Price: 1860}, size{Name: "Party, 60x40 cm", Price: 2410}}
	pizza12 := menuItem{Name: "Pizza Outback", Description: "mit Taco Beef, Schinken, Zwiebeln, Jalapenos und Ei", Sizes: size12, Extras: extrasAll}

	size13 := []size{size{Name: "klein, 24 cm", Price: 850}, size{Name: "groß, 32 cm", Price: 1040}, size{Name: "Family, 45x32 cm", Price: 1860}, size{Name: "Party, 60x40 cm", Price: 2410}}
	pizza13 := menuItem{Name: "Pizza Beverly Hills", Description: "mit Hühnerbrust, Taco Beef, Broccoli, Ananas und Barbecuesauce", Sizes: size13, Extras: extrasAll}

	size14 := []size{size{Name: "klein, 24 cm", Price: 850}, size{Name: "groß, 32 cm", Price: 1040}, size{Name: "Family, 45x32 cm", Price: 1860}, size{Name: "Party, 60x40 cm", Price: 2410}}
	pizza14 := menuItem{Name: "Pizza Speciale", Description: "mit Schinken, Salami, Champignons, Paprika und Ei", Sizes: size14, Extras: extrasAll}

	size15 := []size{size{Name: "klein, 24 cm", Price: 900}, size{Name: "groß, 32 cm", Price: 1100}, size{Name: "Family, 45x32 cm", Price: 1970}, size{Name: "Party, 60x40 cm", Price: 2590}}
	pizza15 := menuItem{Name: "Pizza Full House", Description: "mit Schinken, Peperoniwurst, Speck, Paprika, Peperoni und Ei", Sizes: size15, Extras: extrasAll}

	size16 := []size{size{Name: "klein, 24 cm", Price: 600}, size{Name: "groß, 32 cm", Price: 740}, size{Name: "Family, 45x32 cm", Price: 1310}, size{Name: "Party, 60x40 cm", Price: 1510}}
	pizza16 := menuItem{Name: "Pizza Basic/Wunschpizza", Description: "mit Pizzasauce und Käse", Sizes: size16, Extras: extrasAll}

	json.NewEncoder(w).Encode([]menuItem{pizza1, pizza2, pizza3, pizza4, pizza5, pizza6, pizza7, pizza8, pizza9, pizza10, pizza11, pizza12, pizza13, pizza14, pizza15, pizza16})
}

func Pdf(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/pdf")

	pdf := gofpdf.New("P", "mm", "A4", "")
	pdf.SetAuthor("Christoph Müller", false)
	pdf.AddPage()
	pdf.SetFont("Times", "", 16)

	tr := pdf.UnicodeTranslatorFromDescriptor("")

	message := "Treffpunkt 8 (blaues Schild) / Parkplatz Informatik, bitte anrufen wenn da, wir kommen dann raus"
	addressTxt := fmt.Sprintf("%s\nTheodor-Boveri-Weg\n97074 Würzburg\n%s\n\nBemerkung:\n%s\n\nZahlung: Bar\n\n", addressVal.Name, addressVal.Number, message)

	pdf.MultiCell(0, 8, tr(addressTxt), "", "", false)

	// if only go would be functional ...
	for _, o := range orders {
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

	pdf.Output(w)
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

	addressVal = address

	client := &http.Client{}

	form := url.Values{}
	form.Add("To", c.Phone)
	form.Add("From", "+4993161569016")
	form.Add("MediaUrl", "https://pizza.raytracer.me/pdf")
	form.Add("StatusCallback", "http://pizzas.raytracer.me/updateStatus")

	req, err := http.NewRequest("POST", "https://fax.twilio.com/v1/Faxes", strings.NewReader(form.Encode()))

	if err != nil {
		json.NewEncoder(w).Encode(false)
		return
	}

	req.SetBasicAuth(c.Username, c.Password)
	req.Header.Add("Content-Type", "application/x-www-form-urlencoded")

	resp, err := client.Do(req)
	if err != nil {
		json.NewEncoder(w).Encode(false)
		return
	}

	bodyText, err := ioutil.ReadAll(resp.Body)
	println(string(bodyText))

	json.NewEncoder(w).Encode(true)
}

func UpdateStatus(w http.ResponseWriter, r *http.Request, _ httprouter.Params) {
	w.Header().Set("Content-Type", "application/json")

	bodyText, _ := ioutil.ReadAll(r.Body)
	println(string(bodyText))
}

func main() {
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
	router.GET("/", makeGzipHandler(Index))
	router.GET("/admin"+key, makeGzipHandler(Admin))
	router.GET("/items", makeGzipHandler(Items))
	router.POST("/admin"+key, makeGzipHandler(ChangePayed))
	router.POST("/deleteOrder"+key, makeGzipHandler(DeleteOrder))
	router.GET("/order", makeGzipHandler(Order))
	router.GET("/myorder/:order", makeGzipHandler(MyOrder))
	router.POST("/order", makeGzipHandler(SendOrder))
	router.GET("/orders", makeGzipHandler(Index))
	router.GET("/pdf", makeGzipHandler(Pdf))
	router.POST("/faxorder"+key, makeGzipHandler(FaxOrder))
	router.POST("/updateStatus", makeGzipHandler(UpdateStatus))
	router.GET("/public/*filepath", makeGzipHandler(ServeStatic))

	go func() {
		log.Fatal(http.ListenAndServeTLS(":443", "/etc/letsencrypt/live/pizza.raytracer.me/fullchain.pem", "/etc/letsencrypt/live/pizza.raytracer.me/privkey.pem", router))
	}()
	//No HSTS for now, Pull Requests are welcome
	log.Fatal(http.ListenAndServe(":80", router))
}
