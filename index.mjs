import AWS from "aws-sdk";
import * as ynab from "ynab";
import { simpleParser } from "mailparser";
import { convert } from "html-to-text";
import { GoogleGenerativeAI } from "@google/generative-ai";

const s3 = new AWS.S3();
const secretsManager = new AWS.SecretsManager();
const { SecretString } = await secretsManager
  .getSecretValue({
    SecretId: process.env.SECRET_NAME,
  })
  .promise();
const { YNAB_ACCESS_TOKEN, GEMINI_API_KEY } = JSON.parse(SecretString);
const ynabAPI = new ynab.API(YNAB_ACCESS_TOKEN);

const tangerineCreditCard = {
  ynabAccountId: process.env.YNAB_TANGERINE_ACCOUNT_ID,
  emailAddress: "donotreply@tangerine.ca",
  emailSubject: "A new Credit Card transaction has been made",
};

const bmoCreditCard = {
  ynabAccountId: process.env.YNAB_BMO_ACCOUNT_ID,
  emailAddress: "bmoalerts@bmo.com",
  emailSubject: "BMO Credit Card Alert",
};

export const handler = async (event) => {
  const notification = JSON.parse(event.Records[0].Sns.Message);
  const object = await s3
    .getObject({
      Bucket: process.env.S3_BUCKET_NAME,
      Key: notification.mail.messageId,
    })
    .promise();
  const message = await simpleParser(object.Body);
  const text = convert(message.html);

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

  const payees = (
    await ynabAPI.payees.getPayees(process.env.YNAB_BUDGET_ID)
  ).data.payees
    .filter((payee) => !payee.transfer_account_id && !payee.deleted)
    .map((payee) => payee.name)
    .join("\n");

  const genAI = new GoogleGenerativeAI(GEMINI_API_KEY);
  const model = genAI.getGenerativeModel({
    model: "gemini-1.5-flash",
    systemInstruction: `You will be provided with a credit card alert, and your task is to extract the amount and merchant from it. Once you have extracted the merchant, match it to a payee from the provided list. If no match is found, create a new human-readable payee using the merchant name, ensuring it's less than 200 characters. PayPal, Paddle (PADDLE.NET), FastSpring (FS), and Square (SQ) are payment processors, not merchants. Write your output in JSON format with "amount" and "payee" keys.
    Payee overrides:
    SOBEY'S = Sobeys
    HEATHER'S YIG or WOLFVILLE SAVE EASY = Independent
    WF = Wayfair
    CIRCLE K / IRVING = Irving Oil
    NEW MINAS SUPERS or KINGSTON SUPERSTORE = Real Canadian Superstore
    SOBEYS FAST FUEL = Fast Fuel
    NEEDS CAR WASH = Fast Fuel
    Payees:
    ${payees}`,
  });

  const generationConfig = {
    temperature: 1,
    topP: 0.95,
    topK: 64,
    maxOutputTokens: 8192,
    responseMimeType: "application/json",
  };

  const chatSession = model.startChat({
    generationConfig,
  });

  const { response } = await chatSession.sendMessage(text);
  const { amount, payee } = JSON.parse(response.text());

  await ynabAPI.transactions.createTransaction(process.env.YNAB_BUDGET_ID, {
    transaction: {
      account_id: accountId,
      date: message.date
        .toLocaleString("en-CA", { timeZone: "America/Halifax" })
        .split(",")[0],
      amount: Math.ceil(parseFloat(amount) * -1000),
      payee_name: payee,
      cleared: "uncleared",
    },
  });
};
