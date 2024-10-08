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
import jsonLogic from "json-logic-js";
import { DateTime } from "luxon";
import Handlebars from "handlebars";

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
    if (!notification.mail.messageId) {
      console.error(JSON.stringify(event));
      return console.error("Message ID not found");
    }
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
    if (!message.date) {
      return console.error("Message has no date");
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

    const TransactionExtraction = z.object({
      amount: z.number(),
      merchant: z.string(),
    });
    const paymentProcessors = [
      "PAYPAL",
      "PADDLE.NET",
      "FS",
      "SQ",
      "SP",
      "GOOGLE",
      "DOORDASH",
      "TST",
    ];
    const extractMerchantAmountCompletion =
      await openai.beta.chat.completions.parse({
        model: "gpt-4o-mini",
        messages: [
          {
            role: "system",
            content: `You will be provided with a credit card alert. Your task is to extract the amount and merchant from it. ${paymentProcessors.join(", ")} are payment processors, not merchants. You should convert the amount and merchant to the given structure.`,
          },
          { role: "user", content: text },
        ],
        response_format: zodResponseFormat(
          TransactionExtraction,
          "transaction_extraction",
        ),
      });
    if (!extractMerchantAmountCompletion.choices[0].message.parsed) {
      return console.error("Error parsing message");
    }
    const { amount, merchant } =
      extractMerchantAmountCompletion.choices[0].message.parsed;
    const { Items } = await dynamoDBClient.send(
      new ScanCommand({
        TableName: process.env.DYNAMODB_TABLE_NAME || "",
      }),
    );
    const overrides =
      Items?.map((item) => ({
        payee: item.payee?.S || "",
        category: item.category?.S || "",
        memo: item.memo?.S || "",
        query: item.query?.S || "",
        updatedAt: item.updatedAt?.S || "",
      })) || [];
    const sortedOverrides = overrides.sort(
      (a, b) =>
        new Date(b.updatedAt).getTime() - new Date(a.updatedAt).getTime(),
    );

    const override = sortedOverrides.find((override) => {
      const query = JSON.parse(override.query);
      const now = DateTime.fromJSDate(message.date || new Date()).setZone(
        "America/Halifax",
      );
      return jsonLogic.apply(query, {
        amount,
        merchant: merchant.toUpperCase(),
        day: now.day,
        month: now.month,
      });
    });

    if (override) {
      const template = Handlebars.compile(override.memo);
      Handlebars.registerHelper("formatDate", function (date, format) {
        return DateTime.fromISO(date).toFormat(format);
      });
      Handlebars.registerHelper("subtractMonthFromDate", function (date) {
        return DateTime.fromISO(date).minus({ month: 1 }).toISODate();
      });

      return await ynabAPI.transactions.createTransaction(
        process.env.YNAB_BUDGET_ID || "",
        {
          transaction: {
            account_id: accountId,
            date:
              DateTime.fromJSDate(message.date || new Date())
                .setZone("America/Halifax")
                .toISODate() || undefined,
            amount: Math.round(amount * -1000),
            payee_id: override.payee,
            cleared: "uncleared",
            category_id: override.category || undefined,
            memo: override.memo
              ? template({
                  date: DateTime.fromJSDate(message.date || new Date())
                    .setZone("America/Halifax")
                    .toISODate(),
                })
              : undefined,
          },
        },
      );
    }

    const payees = (
      await ynabAPI.payees.getPayees(process.env.YNAB_BUDGET_ID || "")
    ).data.payees
      .filter((payee) => !payee.transfer_account_id && !payee.deleted)
      .map((payee) => payee.name);

    const Payee = z.object({
      payee: z.string(),
    });
    const matchPayeeCompletion = await openai.beta.chat.completions.parse({
      model: "gpt-4o-mini",
      messages: [
        {
          role: "system",
          content: `Payees:
${JSON.stringify(payees)}
You will be provided with a merchant. Your task is to identify and select the most similar payee from the list. If no match is found, create a new human-readable payee using the merchant name. You should convert the payee to the given structure.`,
        },
        { role: "user", content: merchant },
      ],
      response_format: zodResponseFormat(Payee, "payee"),
    });
    if (!matchPayeeCompletion.choices[0].message.parsed) {
      return console.error("Error matching payee");
    }
    const { payee } = matchPayeeCompletion.choices[0].message.parsed;

    return await ynabAPI.transactions.createTransaction(
      process.env.YNAB_BUDGET_ID || "",
      {
        transaction: {
          account_id: accountId,
          date:
            DateTime.fromJSDate(message.date || new Date())
              .setZone("America/Halifax")
              .toISODate() || undefined,
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
