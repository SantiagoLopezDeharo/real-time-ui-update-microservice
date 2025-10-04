const axios = require("axios");
const crypto = require("crypto");
const inquirer = require("inquirer");
const chalk = require("chalk");
const path = require("path");

// Load environment variables with explicit path
require("dotenv").config({ path: path.resolve(__dirname, ".env") });

// Configuration
const config = {
  apiUrl: process.env.API_URL || "http://localhost:8080/update",
  publishUrl: process.env.PUBLISH_URL || "http://localhost:8080/publish",
  timeTokenSecret: process.env.TIME_TOKEN_SECRET || "your-time-token-secret",
  timeWindow: parseInt(process.env.TIME_WINDOW_SECONDS) || 3600,
};

// Generate time-based token with URL-safe base64 encoding
function generateTimeToken() {
  const currentWindow = Math.floor(Date.now() / 1000 / config.timeWindow);
  const hmac = crypto.createHmac("sha256", config.timeTokenSecret);
  hmac.update(currentWindow.toString());

  // Use URL-safe base64 encoding (replace + with -, / with _, remove padding)
  let mac = hmac.digest("base64");
  mac = mac.replace(/\+/g, "-").replace(/\//g, "_").replace(/=+$/, "");

  const token = `${currentWindow}:${mac}`;

  // URL-safe base64 encode the entire token
  let encodedToken = Buffer.from(token).toString("base64");
  encodedToken = encodedToken
    .replace(/\+/g, "-")
    .replace(/\//g, "_")
    .replace(/=+$/, "");

  console.log("Generated token:", encodedToken);
  console.log("Window:", currentWindow);
  console.log("MAC:", mac);

  return encodedToken;
}

// Send order to microservice (private requests can include a channelName)
async function sendOrder(order, channelName) {
  try {
    const token = generateTimeToken();
    let url = config.apiUrl;
    if (channelName && channelName !== '') {
      url = `${url}?channel=${encodeURIComponent(channelName)}`;
    }

    const response = await axios.post(url, order, {
      headers: {
        "Content-Type": "application/json",
        "X-API-Token": token,
      },
      timeout: 5000,
    });

    return { success: true, data: response.data };
  } catch (error) {
    return {
      success: false,
      error: error.response?.data || error.message,
    };
  }
}

// Rest of the code remains the same...
function generateRandomOrder() {
  const items = [
    "Laptop Computer",
    "Smartphone",
    "Headphones",
    "Coffee Maker",
    "Desk Chair",
    "Wireless Mouse",
    "External Hard Drive",
    "Webcam",
    "Keyboard",
    "Monitor",
    "Tablet",
    "Smart Watch",
  ];

  const randomItem = items[Math.floor(Math.random() * items.length)];
  const randomAmount = Math.floor(Math.random() * 1000) + 50;
  const randomId = `order-${Date.now()}-${Math.floor(Math.random() * 1000)}`;

  return {
    id: randomId,
    item: randomItem,
    amount: randomAmount,
  };
}

// Interactive menu
async function showMenu() {
  console.clear();
  console.log(chalk.blue.bold("🛒 Backend Order Simulator"));
  console.log(chalk.gray("Microservice URL:"), config.apiUrl);
  console.log(
    chalk.gray("Time Token Secret:"),
    config.timeTokenSecret.substring(0, 10) + "..."
  );
  console.log("");

  const { action } = await inquirer.prompt([
    {
      type: "list",
      name: "action",
      message: "Select an action:",
      choices: [
        { name: "Send single order", value: "single" },
        { name: "Send multiple orders", value: "multiple" },
        { name: "Send random order", value: "random" },
        { name: "Exit", value: "exit" },
      ],
    },
  ]);

  if (action === "exit") {
    console.log(chalk.yellow("Goodbye!"));
    process.exit(0);
  }

  // Ask which channel to use for publishing (private = /update, public = /publish)
  const { channel } = await inquirer.prompt([
    {
      type: "list",
      name: "channel",
      message: "Select channel to publish to:",
      choices: [
        { name: "Private (backend) - requires time token", value: "private" },
        { name: "Public - no authentication", value: "public" },
      ],
      default: "private",
    },
  ]);

  const targetUrl = channel === "private" ? config.apiUrl : config.publishUrl;

  // Ask for logical channel name to publish into
  const { channelName } = await inquirer.prompt([
    {
      type: "input",
      name: "channelName",
      message: "Channel name to publish to (default):",
      default: "default",
    },
  ]);

  if (action === "single") {
    const answers = await inquirer.prompt([
      {
        type: "input",
        name: "id",
        message: "Order ID:",
        default: `order-${Date.now()}`,
      },
      {
        type: "input",
        name: "item",
        message: "Item:",
        default: "Test Product",
      },
      {
        type: "number",
        name: "amount",
        message: "Amount:",
        default: 100,
      },
    ]);

    const result = await (async () => {
      if (channel === "private") return await sendOrder(answers, channelName);
      // Public: POST to publishUrl with channel query (include time token so server can validate origin)
      try {
        const token = generateTimeToken();
        await axios.post(`${targetUrl}?channel=${encodeURIComponent(channelName)}`, answers, { headers: { "Content-Type": "application/json", "X-API-Token": token }, timeout: 5000 });
        return { success: true };
      } catch (err) {
        return { success: false, error: err.response?.data || err.message };
      }
    })();

    if (result.success) {
      console.log(chalk.green("✅ Order sent successfully!"));
    } else {
      console.log(chalk.red("❌ Failed to send order:"), result.error);
    }

    await waitForInput();
  }

  if (action === "multiple") {
    const { count } = await inquirer.prompt([
      {
        type: "number",
        name: "count",
        message: "How many orders to send?",
        default: 5,
        validate: (value) => value > 0 || "Please enter a positive number",
      },
    ]);

    console.log(chalk.blue(`Sending ${count} orders...`));

    for (let i = 1; i <= count; i++) {
      const order = generateRandomOrder();
      const result = await (async () => {
        if (channel === "private") return await sendOrder(order, channelName);
        try {
          const token = generateTimeToken();
          await axios.post(`${targetUrl}?channel=${encodeURIComponent(channelName)}`, order, { headers: { "Content-Type": "application/json", "X-API-Token": token }, timeout: 5000 });
          return { success: true };
        } catch (err) {
          return { success: false, error: err.response?.data || err.message };
        }
      })();

      if (result.success) {
        console.log(
          chalk.gray(`[${i}/${count}]`),
          chalk.green(`Order ${order.id} sent`)
        );
      } else {
        console.log(
          chalk.gray(`[${i}/${count}]`),
          chalk.red(`Failed: ${result.error}`)
        );
      }

      // Small delay between requests
      await new Promise((resolve) => setTimeout(resolve, 500));
    }

    await waitForInput();
  }

  if (action === "random") {
    const order = generateRandomOrder();
    console.log(chalk.blue("Generated order:"), order);

    const result = await (async () => {
      if (channel === "private") return await sendOrder(order, channelName);
      try {
        const token = generateTimeToken();
        await axios.post(`${targetUrl}?channel=${encodeURIComponent(channelName)}`, order, { headers: { "Content-Type": "application/json", "X-API-Token": token }, timeout: 5000 });
        return { success: true };
      } catch (err) {
        return { success: false, error: err.response?.data || err.message };
      }
    })();

    if (result.success) {
      console.log(chalk.green("✅ Random order sent successfully!"));
    } else {
      console.log(chalk.red("❌ Failed to send order:"), result.error);
    }

    await waitForInput();
  }

  // Show menu again
  showMenu();
}

// Wait for user input
async function waitForInput() {
  console.log("");
  await inquirer.prompt([
    {
      type: "input",
      name: "continue",
      message: "Press Enter to continue...",
    },
  ]);
}

// Start the application
showMenu().catch(console.error);
