import AWS from "aws-sdk";
import * as ynab from "ynab";
import { simpleParser } from "mailparser";
import { convert } from "html-to-text";
import OpenAI from "openai";
import Airtable from "airtable";

const s3 = new AWS.S3();
const secretsManager = new AWS.SecretsManager();
const { SecretString } = await secretsManager
  .getSecretValue({
    SecretId: process.env.SECRET,
  })
  .promise();
const { OPENAI_API_KEY, YNAB_ACCESS_TOKEN, AIRTABLE_API_KEY } =
  JSON.parse(SecretString);
const ynabAPI = new ynab.API(YNAB_ACCESS_TOKEN);
const openai = new OpenAI({
  apiKey: OPENAI_API_KEY,
});

Airtable.configure({ apiKey: AIRTABLE_API_KEY });
const base = Airtable.base(process.env.AIRTABLE_BASE_ID);

const amountRegex = /\$(?<amount>[0-9]+\.[0-9]{2})/;

const tangerineCreditCard = {
  ynabAccountId: process.env.YNAB_TANGERINE_ACCOUNT_ID,
  emailAddress: "donotreply@tangerine.ca",
  emailSubject: "A new Credit Card transaction has been made",
  emailPayeeRegex: /of.*\sat\s(?<payee>.*)\son\s/s,
};

const bmoCreditCard = {
  ynabAccountId: process.env.YNAB_BMO_ACCOUNT_ID,
  emailAddress: "bmoalerts@bmo.com",
  emailSubject: "BMO Credit Card Alert",
  emailPayeeRegex: /of.*\sat\s(?<payee>.*)\swas\s/s,
};

const getOverrides = async () => {
  const payeesTable = base("Payees");
  const overridesTable = base("Overrides");

  console.info("Selecting all payees from Airtable");
  const payeesRecords = await payeesTable
    .select({
      view: "Grid view",
    })
    .all();
  console.debug(
    "Selected all payees from Airtable",
    JSON.stringify(payeesRecords, null, 2),
  );

  console.info("Searching for payees with overrides");
  const payeesRecordsWithOverrides = payeesRecords.filter(
    (record) => !!record.get("Overrides"),
  );
  console.debug(
    "Found payees with overrides",
    JSON.stringify(payeesRecordsWithOverrides, null, 2),
  );

  console.info("Fetching payee overrides from Airtable");
  const results = await Promise.all(
    payeesRecordsWithOverrides.map(async (payee) => {
      const payeeName = payee.get("Name");
      const overrideIds = payee.get("Overrides");

      const overrides = await Promise.all(
        overrideIds.map(async (overrideId) => {
          const override = await overridesTable.find(overrideId);
          return override.get("Credit Card Transaction");
        }),
      );

      return `${overrides.join(" or ")} = ${payeeName}`;
    }),
  );
  console.debug(
    "Fetched payee overrides from Airtable",
    JSON.stringify(results, null, 2),
  );
  return results.join("\n");
};

const newTransaction = async ({ accountId, date, amount, payee }) => {
  console.info("Fetching payees from YNAB");
  const payees = (
    await ynabAPI.payees.getPayees(process.env.YNAB_BUDGET_ID)
  ).data.payees
    .filter((payee) => !payee.transfer_account_id && !payee.deleted)
    .map((payee) => payee.name)
    .join("\n");
  console.debug("Fetched payees from YNAB", JSON.stringify(payees, null, 2));
  console.info("Fetching overrides from Airtable");
  const overrides = await getOverrides();
  console.info("Sending payee matching request to OpenAI");
  const response = await openai.chat.completions.create({
    model: "gpt-4o-mini-2024-07-18",
    messages: [
      {
        role: "system",
        content: [
          {
            type: "text",
            text: `Match the credit card transaction to a payee from the list provided. If no match is found, create a new payee using the merchant or company name, ensuring it's less than 200 characters. Output only the payee name.
PayPal, Paddle, FS and SQ are payment processesors, not merchants.
Overrides:
${overrides}
Payees:
${payees}`,
          },
        ],
      },
      {
        role: "user",
        content: [
          {
            type: "text",
            text: payee,
          },
        ],
      },
    ],
    temperature: 1,
    max_tokens: 75,
    top_p: 1,
    frequency_penalty: 0,
    presence_penalty: 0,
  });
  console.debug("Matched payee with OpenAI", response.choices[0].message.content)

  console.info("Creating new user-entered transaction in YNAB");
  return await ynabAPI.transactions.createTransaction(
    process.env.YNAB_BUDGET_ID,
    {
      transaction: {
        account_id: accountId,
        date: date
          .toLocaleString("en-CA", { timeZone: process.env.TZ })
          .split(",")[0],
        amount: parseFloat(amount) * -1000,
        payee_id: null,
        payee_name: response.choices[0].message.content,
        category_id: null,
        memo: null,
        cleared: "uncleared",
        approved: false,
        flag_color: null,
        subtransactions: [],
        import_id: null,
      },
    },
  );
};

export const handler = async (event) => {
  const notification = JSON.parse(event.Records[0].Sns.Message);
  const object = await s3
    .getObject({
      Bucket: process.env.BUCKET_NAME,
      Key: notification.mail.messageId,
    })
    .promise();
  console.info("Parsing email message");
  const message = await simpleParser(object.Body);
  console.debug("Parsed email message", JSON.stringify(message, null, 2));
  // Tangerine
  if (
    message.from.value[0].address === tangerineCreditCard.emailAddress &&
    message.subject === tangerineCreditCard.emailSubject
  ) {
    console.info("Converting Tangerine email message body to plain text");
    const text = convert(message.html);
    console.debug(
      "Converted Tangerine email message body to plain text",
      JSON.stringify(text, null, 2),
    );
    console.info("Searching message body for amount and payee");
    const { amount } = amountRegex.exec(text).groups;
    const { payee } = tangerineCreditCard.emailPayeeRegex.exec(text).groups;
    console.debug(
      "Found amount and payee in message body",
      JSON.stringify({ amount, payee }, null, 2),
    );
    try {
      await newTransaction({
        accountId: tangerineCreditCard.ynabAccountId,
        date: message.date,
        amount: amount,
        payee: payee,
      });
    } catch (e) {
      console.error("Error importing transaction to YNAB", e);
    }
    // BMO
  } else if (
    message.from.value[0].address === bmoCreditCard.emailAddress &&
    message.subject === bmoCreditCard.emailSubject
  ) {
    console.info("Converting BMO email message body to plain text");
    const text = convert(message.html);
    console.debug(
      "Converted BMO email message body to plain text",
      JSON.stringify(text, null, 2),
    );
    console.info("Searching message body for amount and payee");
    const { amount } = amountRegex.exec(text).groups;
    const { payee } = bmoCreditCard.emailPayeeRegex.exec(text).groups;
    console.debug(
      "Found amount and payee in message body",
      JSON.stringify({ amount, payee }, null, 2),
    );
    try {
      await newTransaction({
        accountId: bmoCreditCard.ynabAccountId,
        date: message.date,
        amount: amount,
        payee: payee,
      });
    } catch (e) {
      console.error("Error importing transaction to YNAB", e);
    }
  } else {
    console.error("Transaction notification not supported");
  }
};
