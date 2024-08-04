import AWS from "aws-sdk";
import * as ynab from "ynab";
import { simpleParser } from "mailparser";
import { convert } from "html-to-text";
import { v4 as uuidv4 } from "uuid";

const s3 = new AWS.S3();
const ynabAPI = new ynab.API(process.env.YNAB_ACCESS_TOKEN);

const bucketName = "nathanfredericks-transactions";
const budgetId = "e0e7f122-6f2f-41f3-9b84-6d8f49fd5eab";

const tangerineCreditCard = {
  ynabAccountId: "b8df1b5f-4163-43ce-9bd0-01cb1f824cf7",
  emailSubject: "A new Credit Card transaction has been made",
  emailRegex: /\$(?<amount>[0-9]+\.[0-9]{2}).*at (?<payee>.*) on/s,
};

const bmoCreditCard = {
  ynabAccountId: "7920fe4c-4deb-4b33-a860-3b2b0d80085f",
  emailSubject: "BMO Credit Card Alert",
  emailRegex: /\$(?<amount>[0-9]{2}\.[0-9]{2}).*at\W(?<payee>.*) was approved/s,
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
    const { amount, payee } = tangerineCreditCard.emailRegex.exec(text).groups;
    try {
      await ynabAPI.transactions.createTransaction(budgetId, {
        transaction: {
          account_id: tangerineCreditCard.ynabAccountId,
          date: message.date.toLocaleString("en-CA").split(",")[0],
          amount: parseFloat(amount) * -1000,
          payee_id: null,
          payee_name: payee,
          category_id: null,
          memo: null,
          cleared: "uncleared",
          approved: false,
          flag_color: null,
          subtransactions: [],
          import_id: uuidv4(),
        },
      });
      return true;
    } catch (e) {
      console.error("Error creating YNAB transaction:", e);
    }
    // BMO
  } else if (message.subject === bmoCreditCard.emailSubject) {
    const text = convert(message.html);
    const { amount, payee } = bmoCreditCard.emailRegex.exec(text).groups;
    try {
      await ynabAPI.transactions.createTransaction(budgetId, {
        transaction: {
          account_id: bmoCreditCard.ynabAccountId,
          date: message.date.toLocaleString("en-CA").split(",")[0],
          amount: parseFloat(amount) * -1000,
          payee_id: null,
          payee_name: payee,
          category_id: null,
          memo: null,
          cleared: "uncleared",
          approved: false,
          flag_color: null,
          subtransactions: [],
          import_id: uuidv4(),
        },
      });
      return true;
    } catch (e) {
      console.error("Error creating YNAB transaction:", e);
    }
  }

  return false;
};
