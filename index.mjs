import AWS from "aws-sdk";
import * as ynab from "ynab";
import { simpleParser } from "mailparser";
import { convert } from "html-to-text";
import OpenAI from "openai";

const s3 = new AWS.S3();
const ynabAPI = new ynab.API(process.env.YNAB_ACCESS_TOKEN);
const openai = new OpenAI({
  apiKey: process.env.OPENAI_API_KEY,
});

const bucketName = "nathanfredericks-transactions";
const budgetId = "e0e7f122-6f2f-41f3-9b84-6d8f49fd5eab";

const amountRegex = /\$(?<amount>[0-9]+\.[0-9]{2})/;

const tangerineCreditCard = {
  ynabAccountId: "b8df1b5f-4163-43ce-9bd0-01cb1f824cf7",
  emailSubject: "A new Credit Card transaction has been made",
  emailPayeeRegex: /of.*\sat\s(?<payee>.*)\son\s/s,
};

const bmoCreditCard = {
  ynabAccountId: "7920fe4c-4deb-4b33-a860-3b2b0d80085f",
  emailSubject: "BMO Credit Card Alert",
  emailPayeeRegex: /of.*\sat\s(?<payee>.*)\swas\s/s,
};

const newTransaction = async ({ accountId, date, amount, payee }) => {
  const payees = (await ynabAPI.payees.getPayees(budgetId)).data.payees
    .filter((payee) => !payee.transfer_account_id && !payee.deleted)
    .map((payee) => payee.name)
    .join("\n");
  const response = await openai.chat.completions.create({
    model: "gpt-4o-mini-2024-07-18",
    messages: [
      {
        role: "system",
        content: [
          {
            type: "text",
            text: `Match the credit card transaction to a payee from the list provided. If no match is found, create a new payee using the merchant name, ensuring it's less than 200 characters. Output only the payee name.
Overrides:
PayPal, Paddle, and FS are payment processesors, not merchants.
WOLFVILLE SAVE EASY or HEATHER'S YIG = Independent
NEW MINAS SUPERS or Atlantic Superstore = Real Canadian Superstore
SOBEYS FAST FUEL or NEEDS CAR WASH = Fast Fuel
SOBEY'S = Sobey's
CIRCLE K / IRVING = Irving Oil
WF = Wayfair
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

  return await ynabAPI.transactions.createTransaction(budgetId, {
    transaction: {
      account_id: accountId,
      date: date
        .toLocaleString("en-CA", { timeZone: "America/Halifax" })
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
  });
};

export const handler = async (event) => {
  const notification = JSON.parse(event.Records[0].Sns.Message);
  const object = await s3
    .getObject({
      Bucket: bucketName,
      Key: notification.mail.messageId,
    })
    .promise();
  const message = await simpleParser(object.Body);

  // Tangerine
  if (message.subject === tangerineCreditCard.emailSubject) {
    const text = convert(message.html);
    const { amount } = amountRegex.exec(text).groups;
    const { payee } = tangerineCreditCard.emailPayeeRegex.exec(text).groups;
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
  } else if (message.subject === bmoCreditCard.emailSubject) {
    const text = convert(message.html);
    const { amount } = amountRegex.exec(text).groups;
    const { payee } = bmoCreditCard.emailPayeeRegex.exec(text).groups;
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
