package ynab

// PayeeChoice names an existing payee by ID, or a new one by name.
type PayeeChoice struct {
	ID   string `json:"id,omitempty"`
	Name string `json:"name"`
}

// Enrichment is everything an import fills beyond the bank's own fields. An
// empty CategoryID leaves YNAB to apply the payee's default category.
// Fields are what an import fills in besides the amount and date.
type Fields struct {
	Payee      PayeeChoice `json:"payee"`
	CategoryID string      `json:"categoryId,omitempty"`
	Memo       string      `json:"memo,omitempty"`
}

type Transaction struct {
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
	ImportPayeeName         *string          `json:"import_payee_name,omitempty"`
	ImportPayeeNameOriginal *string          `json:"import_payee_name_original,omitempty"`
	MatchedTransactionID    *string          `json:"matched_transaction_id,omitempty"`
	Subtransactions         []Subtransaction `json:"subtransactions,omitempty"`
}

// Subtransaction is one line of a split transaction.
type Subtransaction struct {
	ID                string  `json:"id,omitempty"`
	Amount            int64   `json:"amount"`
	Memo              *string `json:"memo"`
	PayeeID           *string `json:"payee_id"`
	CategoryID        *string `json:"category_id"`
	TransferAccountID *string `json:"transfer_account_id"`
	Deleted           bool    `json:"deleted"`
}
