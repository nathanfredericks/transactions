import { GetObjectCommand, S3Client } from "@aws-sdk/client-s3";
import {
  GetSecretValueCommand,
  SecretsManagerClient,
} from "@aws-sdk/client-secrets-manager";
import { DynamoDBClient, ScanCommand } from "@aws-sdk/client-dynamodb";
import * as ynab from "ynab";
import { simpleParser } from "mailparser";
import { convert } from "html-to-text";
import OpenAI from "openai";
import { z } from "zod";
import { zodResponseFormat } from "openai/helpers/zod";
import { SNSEvent } from "aws-lambda";

const s3Client = new S3Client();
const dynamoDBClient = new DynamoDBClient();
const secretsManagerClient = new SecretsManagerClient();
const { SecretString } = await secretsManagerClient.send(
  new GetSecretValueCommand({
    SecretId: process.env.SECRET_NAME || "",
  }),
);
if (!SecretString) {
  throw new Error("Secrets not found");
}
const { YNAB_ACCESS_TOKEN, OPENAI_API_KEY } = JSON.parse(SecretString);
const ynabAPI = new ynab.API(YNAB_ACCESS_TOKEN);
const openai = new OpenAI({
  apiKey: OPENAI_API_KEY,
});

const tangerineCreditCard = {
  ynabAccountId: process.env.YNAB_TANGERINE_ACCOUNT_ID || "",
  emailAddress: "donotreply@tangerine.ca",
  emailSubject: "A new Credit Card transaction has been made",
};

const bmoCreditCard = {
  ynabAccountId: process.env.YNAB_BMO_ACCOUNT_ID || "",
  emailAddress: "bmoalerts@bmo.com",
  emailSubject: "BMO Credit Card Alert",
};

export const handler = async (event: SNSEvent) => {
  try {
    const notification = JSON.parse(event.Records[0].Sns.Message);
    const { Body } = await s3Client.send(
      new GetObjectCommand({
        Bucket: process.env.S3_BUCKET_NAME || "",
        Key: notification.mail.messageId,
      }),
    );
    if (!Body) {
      return console.error("Message not found");
    }

    const byteArray = await Body.transformToByteArray();
    const message = await simpleParser(Buffer.from(byteArray));
    if (!message.html) {
      return console.error("Message has no HTML content");
    }
    const text = convert(message.html, {
      selectors: [
        { selector: "a", options: { ignoreHref: true } },
        {
          selector: "img",
          format: "skip",
        },
      ],
    });
    if (!message.from) {
      return console.error("Message has no sender");
    }
    const isTangerineNotification =
      message.from.value[0].address === tangerineCreditCard.emailAddress &&
      message.subject === tangerineCreditCard.emailSubject;
    const isBmoNotification =
      message.from.value[0].address === bmoCreditCard.emailAddress &&
      message.subject === bmoCreditCard.emailSubject;

    if (!isTangerineNotification && !isBmoNotification) {
      return console.error("Bank not supported");
    }

    const accountId = isTangerineNotification
      ? tangerineCreditCard.ynabAccountId
      : bmoCreditCard.ynabAccountId;

    const { Items } = await dynamoDBClient.send(
      new ScanCommand({
        TableName: process.env.DYNAMODB_TABLE_NAME || "",
      }),
    );
    const formatOverrides = (overrides: typeof Items | undefined) => {
      if (!overrides) {
        return {};
      }
      return overrides.reduce(
        (accumulator: Record<string, string>, currentValue) => {
          if (!currentValue.merchant.S || !currentValue.payee.S) {
            return accumulator;
          }
          const merchant = currentValue.merchant.S;
          accumulator[merchant] = currentValue.payee.S;
          return accumulator;
        },
        {},
      );
    };
    const overrides = formatOverrides(Items);

    const payees = (
      await ynabAPI.payees.getPayees(process.env.YNAB_BUDGET_ID || "")
    ).data.payees
      .filter((payee) => !payee.transfer_account_id && !payee.deleted)
      .map((payee) => payee.name);

    const TransactionExtraction = z.object({
      amount: z.number(),
      payee: z.string(),
    });

    const paymentProcessors = [
      "PayPal",
      "Paddle (PADDLE.NET)",
      "FastSpring (FS)",
      "Square (SQ)",
      "Shop Pay (SP)",
      "Google (GOOGLE)",
    ];

    const completion = await openai.beta.chat.completions.parse({
      model: "gpt-4o-2024-08-06",
      messages: [
        {
          role: "system",
          content: `You will be provided with a credit card alert, and your task is to extract the amount and merchant from it. Once you have extracted the merchant, check if the merchant has an override. If the merchant does not have an override, match it to a similar payee from the provided list. If no match is found, create a new human-readable payee using the merchant name, ensuring it's less than 200 characters. You should convert the amount and payee to the given structure.
Payment processors:
${JSON.stringify(paymentProcessors)}
Payees:
${JSON.stringify(payees)}
Payee overrides:
${JSON.stringify(overrides)}`,
        },
        { role: "user", content: text },
      ],
      response_format: zodResponseFormat(
        TransactionExtraction,
        "transaction_extraction",
      ),
    });
    if (!completion.choices[0].message.parsed) {
      return console.error("Error parsing message");
    }
    const { amount, payee } = completion.choices[0].message.parsed;

    if (!message.date) {
      return console.error("Message has no date");
    }

    let category_id;
    let memo;
    switch (payee) {
      case "Apple":
        switch (message.date.getDate()) {
          case 9:
            if (amount === 28.74) {
              memo = "ChatGPT Plus";
              category_id = process.env.YNAB_MONTHLY_SUBSCRIPTIONS_CATEGORY_ID;
            }
            break;
          case 21:
            if (amount === 6.89) {
              memo = "Apple Music";
              category_id = process.env.YNAB_MONTHLY_SUBSCRIPTIONS_CATEGORY_ID;
            }
            break;
          case 22:
            if (amount === 1.48) {
              memo = "iCloud Storage";
              category_id = process.env.YNAB_MONTHLY_SUBSCRIPTIONS_CATEGORY_ID;
            }
            break;
          case 29:
            if (amount === 17.24) {
              memo = "StrongLifts";
              category_id = process.env.YNAB_CERTN_FLEXFUND_CATEGORY_ID;
            }
            break;
        }
        break;
      case "Amazon":
        switch (message.date.getDate()) {
          case 5:
            if (amount === 5.74) {
              memo = "Amazon Prime";
              category_id = process.env.YNAB_MONTHLY_SUBSCRIPTIONS_CATEGORY_ID;
            }
            break;
        }
        break;
      case "Amazon Web Services":
      case "Oracle Cloud":
      case "Hetzner Cloud":
        const messageDate = message.date;
        messageDate.setMonth(messageDate.getMonth() - 1);
        const months = [
          "January",
          "February",
          "March",
          "April",
          "May",
          "June",
          "July",
          "August",
          "September",
          "October",
          "November",
          "December",
        ];
        memo = `${months[messageDate.getMonth()]} ${messageDate.getFullYear()}`;
        category_id = process.env.YNAB_CLOUD_SERVICES_CATEGORY_ID;
        break;
    }

    await ynabAPI.transactions.createTransaction(
      process.env.YNAB_BUDGET_ID || "",
      {
        transaction: {
          account_id: accountId,
          date: message.date
            .toLocaleString("en-CA", { timeZone: "America/Halifax" })
            .split(",")[0],
          amount: Math.round(amount * -1000),
          payee_name: payee,
          cleared: "uncleared",
          category_id,
          memo,
        },
      },
    );
  } catch (e) {
    console.error(e);
  }
};
