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
	"path"
	"strconv"
	"strings"
	"time"

	"github.com/Azure/azure-sdk-for-go/sdk/data/aztables"
	"github.com/chromedp/chromedp"
)

func (sc *ServiceContainer) tick() {

	fmt.Print("Enter tick.\n")

configs:
	for i, config := range sc.Configs {

		fmt.Printf("Iterating over config %d...\n", i)

		ctx, cancel := context.WithTimeout(context.Background(), 1*time.Minute)
		defer cancel()

		// Query the page
		allocatorCtx, _ := chromedp.NewExecAllocator(
			ctx,
			append([]func(allocator *chromedp.ExecAllocator){
				chromedp.ExecPath(sc.ChromiumPath),
			}, chromedp.DefaultExecAllocatorOptions[:]...)...,
		)

		dpctx, cancel := chromedp.NewContext(allocatorCtx)
		defer cancel()

		var items []string
		err := chromedp.Run(dpctx,
			chromedp.Navigate(config.TargetUrl),
			chromedp.Sleep(time.Duration(config.WaitSeconds)*time.Second),
			chromedp.EvaluateAsDevTools(config.StringArrayJs, &items),
		)

		if err != nil {
			fmt.Printf("Could not query TargetUrl %s using StringArrayJs %s: %v\n", config.TargetUrl, config.StringArrayJs, err)
			return
		}

		// Read from storage
		_, err = sc.AzureTablesServiceClient.CreateTable(ctx, config.AzureStorageTableName, nil)
		if err != nil && !strings.Contains(err.Error(), "TableAlreadyExists") {
			log.Fatalf("Could not create table %s: %v\n", config.AzureStorageTableName, err)
		}

		tableClient := sc.AzureTablesServiceClient.NewClient(config.AzureStorageTableName)
		pager := tableClient.NewListEntitiesPager(&aztables.ListEntitiesOptions{})
		existing := make([]aztables.EDMEntity, 0)
		for pager.More() {
			res, err := pager.NextPage(ctx)
			if err != nil {
				fmt.Printf("Could not query entities from table %s: %v\n", config.AzureStorageTableName, err)
				break
			}

			for _, bytes := range res.Entities {
				var entity aztables.EDMEntity
				err := json.Unmarshal(bytes, &entity)
				if err != nil {
					log.Fatalf("Could not unmarshal an entity from table %s: %v\n", config.AzureStorageTableName, err)
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
			fmt.Printf("Notifying for %d items...\n", len(toNotify))
			pushBulletReqBodyMap := map[string]interface{}{
				"title": config.NotificationTitle,
				"body":  strings.Join(toNotify, ", "),
				"type":  "note",
			}
			pushBulletReqBodyBytes, err := json.Marshal(pushBulletReqBodyMap)
			if err != nil {
				log.Fatalf("Could not marshal pushBulletReqBodyMap %v: %v\n", pushBulletReqBodyMap, err)
			}

			pushBulletReq, err := http.NewRequestWithContext(ctx, http.MethodPost, "/v2/pushes", bytes.NewBuffer(pushBulletReqBodyBytes))
			if err != nil {
				log.Fatalf("Could not create pushBulletReq: %v\n", err)
			}

			pushBulletRes, err := sc.PushBulletClient.Do(pushBulletReq)
			if err != nil {
				fmt.Printf("Push Bullet request failed: %v\n", err)
				continue configs
			}

			if pushBulletRes.StatusCode != 200 {
				fmt.Printf("Push Bullet returned an unexpected status code %d.\n", pushBulletRes.StatusCode)
				continue configs
			}
		} else {
			fmt.Print("Nothing to notify.\n")
		}

		// Save to storage
		if len(toAdd) > 0 {
			fmt.Printf("Adding %d items...\n", len(toAdd))
			for _, a := range toAdd {
				bytes, err := json.Marshal(a)
				if err != nil {
					log.Fatalf("Could not marshal entity: %v\n", err)
				}
				tableClient.AddEntity(ctx, bytes, nil)
			}
		}
	}

	fmt.Print("Exit tick.\n")
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
	ChromiumPath             string
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

	cwd, err := os.Getwd()
	if err != nil {
		log.Fatalf("Failed to get current working directory: %v.\n", err)
	}

	azureStorageConnectionString, ok := os.LookupEnv("AzureWebJobsStorage")
	if !ok {
		log.Fatal("AzureWebJobsStorage not set.\n")
	}

	functionsCustomHandlerPort, ok := os.LookupEnv("FUNCTIONS_CUSTOMHANDLER_PORT")
	if !ok {
		functionsCustomHandlerPort = "8080"
	}

	pushBulletAccessToken, ok := os.LookupEnv("APP_PUSHBULLETACCESSTOKEN")
	if !ok {
		log.Fatal("APP_PUSHBULLETACCESSTOKEN not set.\n")
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
			log.Fatalf("Could not parse WAITSECONDS value: %v\n", err)
		}

		notificationTitle, ok := os.LookupEnv(fmt.Sprintf("APP_%d_NOTIFICATIONTITLE", i))
		if !ok {
			break
		}

		configs = append(configs, Config{azureStorageTableName, targetUrl, stringArrayJs, waitSeconds, notificationTitle})
	}

	if len(configs) == 0 {
		log.Fatal("No configs found.\n")
	}

	azureTablesServiceClient, err := aztables.NewServiceClientFromConnectionString(azureStorageConnectionString, nil)
	if err != nil {
		log.Fatalf("Could not build Azure Tables service client: %v\n", err)
	}

	pushBulletBaseURLStr := "https://api.pushbullet.com/"
	pushBulletBaseURL, err := url.Parse(pushBulletBaseURLStr)
	if err != nil {
		log.Fatalf("Could not parse pushBulletBaseURLStr %s: %v\n", pushBulletBaseURLStr, err)
	}

	pushBulletClient := http.Client{
		Transport: &PushBulletTransport{
			AccessToken:    pushBulletAccessToken,
			BaseURL:        pushBulletBaseURL,
			InnerTransport: http.DefaultTransport,
		}}

	sc := ServiceContainer{configs, azureTablesServiceClient, &pushBulletClient, path.Join(cwd, "chrome-linux", "chrome")}

	addr := fmt.Sprintf(":%s", functionsCustomHandlerPort)

	http.HandleFunc("/timer", func(w http.ResponseWriter, r *http.Request) {
		sc.tick()
		w.WriteHeader(201)
	})
	log.Fatal(http.ListenAndServe(addr, nil))
}
