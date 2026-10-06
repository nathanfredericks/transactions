// operator invokes the authoritative Go backend. It never reads bank sessions.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/service/lambda"
	"github.com/nathanfredericks/transactions/internal/config"
	"github.com/nathanfredericks/transactions/internal/engine"
	"os"
	"time"
)

func main() {
	action := flag.String("action", "banks.list", "Backend action")
	bank := flag.String("bank", "", "Registered bank")
	job := flag.String("job", "", "Durable job ID")
	payload := flag.String("payload", "", "JSON file containing action payload")
	function := flag.String("function", "transactions-engine-gateway", "Gateway function")
	flag.Parse()
	ctx, cancel := context.WithTimeout(context.Background(), 190*time.Second)
	defer cancel()
	cfg, err := config.GetAWSConfig(ctx)
	check(err)
	r := engine.Request{Action: *action, Bank: *bank, JobID: *job}
	if *payload != "" {
		r.Payload, err = os.ReadFile(*payload)
		check(err)
		if !json.Valid(r.Payload) {
			check(fmt.Errorf("invalid JSON"))
		}
	}
	if *action == "submit" {
		var j engine.Job
		check(json.Unmarshal(r.Payload, &j))
		r.Job = &j
		r.Payload = nil
	}
	data, err := json.Marshal(r)
	check(err)
	result, err := lambda.NewFromConfig(cfg).Invoke(ctx, &lambda.InvokeInput{FunctionName: aws.String(*function), Payload: data})
	check(err)
	if result.FunctionError != nil {
		check(fmt.Errorf("backend rejected operation; inspect job or CloudWatch logs"))
	}
	var value any
	check(json.Unmarshal(result.Payload, &value))
	out, err := json.MarshalIndent(value, "", "  ")
	check(err)
	fmt.Println(string(out))
}
func check(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
