# beenotif
Web scraper push notification Azure Function implemented with golang.

It scrapes data from a configured web page with JavaScript (powered by [chromedp](https://github.com/chromedp/chromedp)) and delivers any new results in a push notification (powered by [Push Bullet](https://www.pushbullet.com/)).

This was also a test on how well golang works with Azure Functions https://food.joona.cloud/2023/11/03/Deep-dive-to-Azure-Functions-with-golang

## Running locally
First run the publish script `./publish.sh`. After that you can re-build and run with `go build -o build/ && func start`.

## Publish to Azure
Run the publish script `./publish.sh` and then, in VSCode Azure Tools, navigate to your desired subscription's Function App section and choose `Create Function App in Azure... (Advanced)`.
* Runtime stack = Custom handler
* OS = Linux

## Settings
Here's a sample local.settings.json:

```json
{
  "IsEncrypted": false,
  "Values": {
    "FUNCTIONS_WORKER_RUNTIME": "custom",
    "AzureWebJobsStorage": "REDACTED",
    "APP_PUSHBULLETACCESSTOKEN": "REDACTED",
    "APP_0_AZURESTORAGETABLENAME": "somesitedata",
    "APP_0_TARGETURL": "https://somesite.com",
    "APP_0_WAITSECONDS": "10",
    "APP_0_STRINGARRAYJS": "Array.from(Array.from(document.querySelectorAll('span')).find(el => el.textContent === 'Please pick one of the following times:')?.parentElement.children).filter(el => el.nodeName === 'BUTTON').map(el => el.innerText.split(' ')[1]).filter(date => Date.parse(date.split('.').reverse().join('-')) < Date.parse('2000-01-01'))",
    "APP_0_NOTIFICATIONTITLE": "Hey! We got some new data for ya."
  }
}
```
