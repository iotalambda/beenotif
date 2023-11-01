package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
	"github.com/chromedp/chromedp"
)

func (sc *ServiceContainer) tick() {

	log.Print("Enter tick.")

configs:
	for i, config := range sc.Configs {

		log.Printf("Iterating over config %d...", i)

		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
		defer cancel()

		// Query page
		dpctx, cancel := chromedp.NewContext(ctx)
		defer cancel()

		var items []string
		err := chromedp.Run(dpctx,
			chromedp.Navigate(config.TargetUrl),
			chromedp.Sleep(time.Duration(config.WaitSeconds)*time.Second),
			chromedp.EvaluateAsDevTools(config.StringArrayJs, &items),
		)

		if err != nil {
			log.Printf("Could not query TargetUrl %s using StringArrayJs %s: %v", config.TargetUrl, config.StringArrayJs, err)
			return
		}

		// Read from storage
		_, err = sc.AzureTablesServiceClient.CreateTable(ctx, config.AzureStorageTableName, nil)
		if err != nil && !strings.Contains(err.Error(), "TableAlreadyExists") {
			log.Fatalf("Could not create table %s: %v", config.AzureStorageTableName, err)
		}

		tableClient := sc.AzureTablesServiceClient.NewClient(config.AzureStorageTableName)
		pager := tableClient.NewListEntitiesPager(&aztables.ListEntitiesOptions{})
		existing := make([]aztables.EDMEntity, 0)
		for pager.More() {
			res, err := pager.NextPage(ctx)
			if err != nil {
				log.Printf("Could not query entities from table %s: %v", config.AzureStorageTableName, err)
				break
			}

			for _, bytes := range res.Entities {
				var entity aztables.EDMEntity
				err := json.Unmarshal(bytes, &entity)
				if err != nil {
					log.Fatalf("Could not unmarshal an entity from table %s: %v", config.AzureStorageTableName, err)
				}
				existing = append(existing, entity)
			}
		}

		toAdd := make([]aztables.EDMEntity, 0)
		toNotify := make([]string, 0)
	items:
		for _, item := range items {
			for _, e := range existing {
				if item == e.RowKey {
					continue items
				}
			}
			toAdd = append(toAdd, aztables.EDMEntity{
				Entity: aztables.Entity{
					PartitionKey: item,
					RowKey:       item,
				},
			})
			toNotify = append(toNotify, item)
		}

		// Notify
		if len(toNotify) > 0 {
			log.Printf("Notifying for %d items...", len(toNotify))
			pushBulletReqBodyMap := map[string]interface{}{
				"title": config.NotificationTitle,
				"body":  strings.Join(toNotify, ", "),
				"type":  "note",
			}
			pushBulletReqBodyBytes, err := json.Marshal(pushBulletReqBodyMap)
			if err != nil {
				log.Fatalf("Could not marshal pushBulletReqBodyMap %v: %v", pushBulletReqBodyMap, err)
			}

			pushBulletReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "/v2/pushes", bytes.NewBuffer(pushBulletReqBodyBytes))
			if err != nil {
				log.Fatalf("Could not create pushBulletReq: %v", err)
			}

			pushBulletRes, err := sc.PushBulletClient.Do(pushBulletReq)
			if err != nil {
				log.Printf("Push Bullet request failed: %v", err)
				continue configs
			}

			if pushBulletRes.StatusCode != 200 {
				log.Printf("Push Bullet returned an unexpected status code %d.", pushBulletRes.StatusCode)
				continue configs
			}
		} else {
			log.Print("Nothing to notify.")
		}

		// Save to storage
		if len(toAdd) > 0 {
			log.Printf("Adding %d items...", len(toAdd))
			for _, a := range toAdd {
				bytes, err := json.Marshal(a)
				if err != nil {
					log.Fatalf("Could not marshal entity: %v", err)
				}
				tableClient.AddEntity(ctx, bytes, nil)
			}
		}
	}

	log.Print("Exit tick.")
}

type Config struct {
	AzureStorageTableName string
	TargetUrl             string
	StringArrayJs         string
	WaitSeconds           int
	NotificationTitle     string
}

type ServiceContainer struct {
	Configs                  []Config
	AzureTablesServiceClient *aztables.ServiceClient
	PushBulletClient         *http.Client
}

type PushBulletTransport struct {
	AccessToken    string
	BaseURL        *url.URL
	InnerTransport http.RoundTripper
}

func (t *PushBulletTransport) RoundTrip(req *http.Request) (*http.Response, error) {
	req.Header.Add("Access-Token", t.AccessToken)
	req.Header.Add("Content-Type", "application/json")
	URL := t.BaseURL.ResolveReference(req.URL)
	req.URL = URL
	return t.InnerTransport.RoundTrip(req)
}

func main() {

	azureStorageConnectionString, ok := os.LookupEnv("AzureWebJobsStorage")
	if !ok {
		log.Fatal("AzureWebJobsStorage not set.")
	}

	functionsCustomHandlerPort, ok := os.LookupEnv("FUNCTIONS_CUSTOMHANDLER_PORT")
	if !ok {
		functionsCustomHandlerPort = "8080"
	}

	pushBulletAccessToken, ok := os.LookupEnv("APP_PUSHBULLETACCESSTOKEN")
	if !ok {
		log.Fatal("APP_PUSHBULLETACCESSTOKEN not set.")
	}

	configs := make([]Config, 0)
	for i := 0; ; i++ {
		azureStorageTableName, ok := os.LookupEnv(fmt.Sprintf("APP_%d_AZURESTORAGETABLENAME", i))
		if !ok {
			break
		}

		targetUrl, ok := os.LookupEnv(fmt.Sprintf("APP_%d_TARGETURL", i))
		if !ok {
			break
		}

		stringArrayJs, ok := os.LookupEnv(fmt.Sprintf("APP_%d_STRINGARRAYJS", i))
		if !ok {
			break
		}

		waitSecondsStr, ok := os.LookupEnv(fmt.Sprintf("APP_%d_WAITSECONDS", i))
		if !ok {
			break
		}

		waitSeconds, err := strconv.Atoi(waitSecondsStr)
		if err != nil {
			log.Fatalf("Could not parse WAITSECONDS value: %v", err)
		}

		notificationTitle, ok := os.LookupEnv(fmt.Sprintf("APP_%d_NOTIFICATIONTITLE", i))
		if !ok {
			break
		}

		configs = append(configs, Config{azureStorageTableName, targetUrl, stringArrayJs, waitSeconds, notificationTitle})
	}

	if len(configs) == 0 {
		log.Fatal("No configs found.")
	}

	azureTablesServiceClient, err := aztables.NewServiceClientFromConnectionString(azureStorageConnectionString, nil)
	if err != nil {
		log.Fatalf("Could not build Azure Tables service client: %v", err)
	}

	pushBulletBaseURLStr := "https://api.pushbullet.com/"
	pushBulletBaseURL, err := url.Parse(pushBulletBaseURLStr)
	if err != nil {
		log.Fatalf("Could not parse pushBulletBaseURLStr %s: %v", pushBulletBaseURLStr, err)
	}

	pushBulletClient := http.Client{
		Transport: &PushBulletTransport{
			AccessToken:    pushBulletAccessToken,
			BaseURL:        pushBulletBaseURL,
			InnerTransport: http.DefaultTransport,
		}}

	sc := ServiceContainer{configs, azureTablesServiceClient, &pushBulletClient}

	addr := fmt.Sprintf(":%s", functionsCustomHandlerPort)

	http.HandleFunc("/timer", func(w http.ResponseWriter, r *http.Request) {
		sc.tick()
	})
	log.Fatal(http.ListenAndServe(addr, nil))
}
