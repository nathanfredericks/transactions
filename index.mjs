import AWS from "aws-sdk";
import * as ynab from "ynab";
import { simpleParser } from "mailparser";
import { convert } from "html-to-text";
import { v4 as uuidv4 } from "uuid";

const s3 = new AWS.S3();
const ynabAPI = new ynab.API(process.env.YNAB_ACCESS_TOKEN);

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

const newTransaction = async ({
  accountId,
  date,
  amount,
  payee,
  imported = false,
}) => {
  return await ynabAPI.transactions.createTransaction(budgetId, {
    transaction: {
      account_id: accountId,
      date: date
        .toLocaleString("en-CA", { timeZone: "America/Halifax" })
        .split(",")[0],
      amount: parseFloat(amount) * -1000,
      payee_id: null,
      payee_name: payee,
      category_id: null,
      memo: null,
      cleared: "uncleared",
      approved: false,
      flag_color: null,
      subtransactions: [],
      import_id: imported ? uuidv4() : null,
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
      const transaction = {
        accountId: tangerineCreditCard.ynabAccountId,
        date: message.date,
        amount: amount,
        payee: payee,
      };
      // Create new imported transaction
      const importedTransaction = await newTransaction({
        ...transaction,
        imported: true,
      });
      // Delete imported transaction
      await ynabAPI.transactions.deleteTransaction(
        budgetId,
        importedTransaction.data.transaction.id,
      );
      // Create new user-entered transaction with imported transaction payee
      // User-entered transactions do not follow payee naming rules
      await newTransaction({
        ...transaction,
        payee: importedTransaction.data.transaction.payee_name,
      });
      return null;
    } catch (e) {
      console.error("Error importing transaction to YNAB", e);
    }
    // BMO
  } else if (message.subject === bmoCreditCard.emailSubject) {
    const text = convert(message.html);
    const { amount } = amountRegex.exec(text).groups;
    const { payee } = bmoCreditCard.emailPayeeRegex.exec(text).groups;
    try {
      const transaction = {
        accountId: bmoCreditCard.ynabAccountId,
        date: message.date,
        amount: amount,
        payee: payee,
      };
      // Create new imported transaction
      const importedTransaction = await newTransaction({
        ...transaction,
        imported: true,
      });
      // Delete imported transaction
      await ynabAPI.transactions.deleteTransaction(
        budgetId,
        importedTransaction.data.transaction.id,
      );
      // Create new user-entered transaction with imported transaction payee
      // User-entered transactions do not follow payee naming rules
      await newTransaction({
        ...transaction,
        payee: importedTransaction.data.transaction.payee_name,
      });
      return null;
    } catch (e) {
      console.error("Error importing transaction to YNAB", e);
    }
  } else {
    console.error("Transaction notification not supported");
  }
};
