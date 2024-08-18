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

    const completion = await openai.beta.chat.completions.parse({
      model: "gpt-4o-mini",
      messages: [
        {
          role: "system",
          content: `You will be provided with a credit card alert, and your task is to extract the amount and merchant from it. Once you have extracted the merchant, match it to a payee from the provided list. If no match is found, create a new human-readable payee using the merchant name, ensuring it's less than 200 characters. PayPal, Paddle (PADDLE.NET), FastSpring (FS), Square (SQ) and Shop Pay (SP) are payment processors, not merchants. You should convert the amount and payee to the given structure.
Payees:
${JSON.stringify(payees)}
Overrides:
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
        },
      },
    );
  } catch (e) {
    console.error(e);
  }
};
