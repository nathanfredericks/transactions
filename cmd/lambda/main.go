package main

import (
	"github.com/aws/aws-lambda-go/lambda"
	"github.com/nathanfredericks/transactions/internal/handler"
)

func main() {
	lambda.Start(handler.Handle)
}
