const WebSocket = require("ws");
const jwt = require("jsonwebtoken");
const chalk = require("chalk");
const Table = require("cli-table3");
const path = require('path');
require('dotenv').config({ path: path.resolve(__dirname, '.env') });

// Configuration
const config = {
  wsUrl: process.env.WS_URL || "ws://localhost:8080/ws",
  jwtSecret: process.env.JWT_SECRET || "your-jwt-secret",
  userId: process.env.USER_ID || "demo-user-123",
};

// Generate JWT token
function generateToken() {
  return jwt.sign(
    {
      sub: config.userId,
      name: "Demo User",
      iat: Math.floor(Date.now() / 1000),
    },
    config.jwtSecret,
    { expiresIn: "1h" }
  );
}

// Create CLI table for displaying orders
const ordersTable = new Table({
  head: [
    chalk.cyan("Order ID"),
    chalk.cyan("Item"),
    chalk.cyan("Amount"),
    chalk.cyan("Received At"),
  ],
  colWidths: [20, 30, 10, 20],
});

// Clear console and display header
console.clear();
console.log(chalk.green.bold("📦 Real-Time Order Updates Demo"));
console.log(chalk.gray("Connecting to WebSocket server...\n"));

// Connect to WebSocket
const token = generateToken();
const ws = new WebSocket(`${config.wsUrl}?token=${token}`);

ws.on("open", function open() {
  console.log(chalk.green("✅ Connected to WebSocket server"));
  console.log(chalk.gray("Waiting for order updates...\n"));
});

ws.on("message", function message(data) {
  try {
    const order = JSON.parse(data);
    const timestamp = new Date().toLocaleTimeString();

    // Add order to table
    ordersTable.push([order.id, order.item, `$${order.amount}`, timestamp]);

    // Clear console and redraw table
    console.clear();
    console.log(chalk.green.bold("📦 Real-Time Order Updates Demo"));
    console.log(chalk.gray(`Connected - ${new Date().toLocaleString()}\n`));
    console.log(ordersTable.toString());
    console.log(chalk.gray(`\nTotal orders received: ${ordersTable.length}`));
  } catch (error) {
    console.error("Error parsing message:", error);
  }
});

ws.on("error", function error(err) {
  console.error(chalk.red("WebSocket error:"), err.message);
});

ws.on("close", function close() {
  console.log(chalk.yellow("WebSocket connection closed"));
});

// Handle graceful shutdown
process.on("SIGINT", function () {
  console.log(chalk.yellow("\nShutting down..."));
  ws.close();
  process.exit();
});
