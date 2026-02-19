package types

type MerchantAmount struct {
	Amount   float64 `json:"amount"`
	Merchant string  `json:"merchant"`
}

type PayeeMatch struct {
	Payee string `json:"payee"`
}

type WebhookPayload struct {
	Notification string `json:"notification"`
	Bank         string `json:"bank"`
}

type EmailConfig struct {
	YNABAccountID string   `json:"ynabAccountId"`
	EmailAddress  string   `json:"emailAddress"`
	EmailSubject  string   `json:"emailSubject"`
	Last4         []string `json:"last4"`
}

type WebhookConfig struct {
	YNABAccountID string   `json:"ynabAccountId"`
	Bank          string   `json:"bank"`
	Last4         []string `json:"last4"`
}

type Config struct {
	Email   []EmailConfig   `json:"email"`
	Webhook []WebhookConfig `json:"webhook"`
}

type Secrets struct {
	YNABAccessToken string `json:"YNAB_ACCESS_TOKEN"`
	OpenAIAPIKey    string `json:"OPENAI_API_KEY"`
	PushoverToken   string `json:"PUSHOVER_TOKEN"`
	PushoverUser    string `json:"PUSHOVER_USER"`
}

type Parameters struct {
	PaymentProcessors []string
	Timezone          string
	OpenAIEndpoint    string
	OpenAIModel       string
}

type Env struct {
	AWSSecretARN                         string
	AppConfigApplication                 string
	AppConfigEnvironment                 string
	AppConfigConfiguration               string
	AWSS3BucketName                      string
	YNABBudgetID                         string
	AWSTransactionOverridesDynamoDBTable string
}

type TransactionOverride struct {
	Payee     string `json:"payee"`
	Category  string `json:"category"`
	Memo      string `json:"memo"`
	Query     string `json:"query"`
	UpdatedAt string `json:"updatedAt"`
}

type YNABTransaction struct {
	ID          string  `json:"id"`
	Date        string  `json:"date"`
	Amount      int64   `json:"amount"`
	Cleared     string  `json:"cleared"`
	Approved    bool    `json:"approved"`
	AccountID   string  `json:"account_id"`
	AccountName string  `json:"account_name"`
	Deleted     bool    `json:"deleted"`
	PayeeID     *string `json:"payee_id"`
	PayeeName   *string `json:"payee_name"`
	CategoryID  *string `json:"category_id"`
	Memo        *string `json:"memo"`
}

type SESNotification struct {
	Mail struct {
		MessageID string `json:"messageId"`
	} `json:"mail"`
}
