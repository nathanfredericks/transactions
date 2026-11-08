package types

// PayeeChoice is the payee a transaction is written with. An ID selects an
// existing YNAB payee; a Name without an ID creates a new payee.
type PayeeChoice struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}

// Enrichment is everything an import fills beyond the bank's own fields. An
// empty CategoryID leaves YNAB to apply the payee's default category.
type Enrichment struct {
	Payee      PayeeChoice `json:"payee"`
	CategoryID string      `json:"categoryId,omitempty"`
	Memo       string      `json:"memo,omitempty"`
}

type EmailConfig struct {
	Bank          string   `json:"bank"`
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

// Secrets is the JSON stored in the single Transactions secret. The Lambdas
// use the first five keys; the browser task uses the browser and bank keys.
type Secrets struct {
	WebhookToken           string                     `json:"WEBHOOK_TOKEN"`
	YNABAccessToken        string                     `json:"YNAB_ACCESS_TOKEN"`
	OpenAIAPIKey           string                     `json:"OPENAI_API_KEY"`
	PushoverToken          string                     `json:"PUSHOVER_TOKEN"`
	PushoverUser           string                     `json:"PUSHOVER_USER"`
	CloakBrowserLicenseKey string                     `json:"CLOAKBROWSER_LICENSE_KEY"`
	BrowserBrokerToken     string                     `json:"BROWSER_BROKER_TOKEN"`
	Banks                  map[string]BankCredentials `json:"BANKS"`
}

// BankCredentials matches bank.CredentialsData's JSON so one bank's entry
// keeps the shape its former per-bank secret had.
type BankCredentials struct {
	Username  string `json:"username"`
	Password  string `json:"password"`
	MailToken string `json:"mailToken"`
}

type Parameters struct {
	PaymentProcessors []string
	Timezone          string
	OpenAIEndpoint    string
	OpenAIModel       string
}

type TransactionOverride struct {
	ID        string `json:"id"`
	Name      string `json:"name"`
	Payee     string `json:"payee"`
	Category  string `json:"category"`
	Memo      string `json:"memo"`
	Query     string `json:"query"`
	UpdatedAt string `json:"updatedAt"`
	Revision  string `json:"revision"`
}

type YNABTransaction struct {
	ImportID          *string `json:"import_id"`
	TransferAccountID *string `json:"transfer_account_id"`
	ID                string  `json:"id"`
	Date              string  `json:"date"`
	Amount            int64   `json:"amount"`
	Cleared           string  `json:"cleared"`
	Approved          bool    `json:"approved"`
	AccountID         string  `json:"account_id"`
	AccountName       string  `json:"account_name"`
	Deleted           bool    `json:"deleted"`
	PayeeID           *string `json:"payee_id"`
	PayeeName         *string `json:"payee_name"`
	CategoryID        *string `json:"category_id"`
	Memo              *string `json:"memo"`
	FlagColor         *string `json:"flag_color,omitempty"`
	// ImportPayeeName and ImportPayeeNameOriginal keep the bank descriptor YNAB
	// received for an imported transaction, independent of the payee chosen.
	ImportPayeeName         *string              `json:"import_payee_name,omitempty"`
	ImportPayeeNameOriginal *string              `json:"import_payee_name_original,omitempty"`
	MatchedTransactionID    *string              `json:"matched_transaction_id,omitempty"`
	Subtransactions         []YNABSubtransaction `json:"subtransactions,omitempty"`
}

// YNABSubtransaction is one line of a split transaction.
type YNABSubtransaction struct {
	ID                string  `json:"id,omitempty"`
	Amount            int64   `json:"amount"`
	Memo              *string `json:"memo"`
	PayeeID           *string `json:"payee_id"`
	CategoryID        *string `json:"category_id"`
	TransferAccountID *string `json:"transfer_account_id"`
	Deleted           bool    `json:"deleted"`
}
