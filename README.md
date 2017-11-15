# pizza
The pizza order website for the OpenColloq@UniversityOfWuerzburg - https://wuecampus2.uni-wuerzburg.de/moodle/course/view.php?id=21049

This service does not have session management, instead a secret link (the secret is specified in the config file) is used for the admin interface.

Written in a quick and dirty fashion (the price of extras does NOT scale with the size of the pizza, but is instead just added with a helper offset method, this should have been done differently, but requires to change large parts of the code). If you have an improvement proposal, just create an issue or better yet create a pull request!

The code uses the twilio service to send faxes to the pizza delivery service, please provide your own user credentials for this service. 
