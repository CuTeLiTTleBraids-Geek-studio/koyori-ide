#!/usr/bin/env node
// Wait for the Vite dev server to be ready, then launch the Wails app binary.
// Used by wails3 dev to avoid the race condition where the Go binary starts
// before the frontend dev server is accepting connections.
const http = require("http");
const { spawn } = require("child_process");
const path = require("path");

const PORT = Number(process.env.WAILS_VITE_PORT) || 9245;
const MAX_RETRIES = 60;
const RETRY_INTERVAL = 1000; // ms
const binaryPath = path.resolve(__dirname, "..", "bin", "koyori-ide.exe");

let retries = 0;

function check() {
  const req = http.get(
    { hostname: "127.0.0.1", port: PORT, path: "/", timeout: 2000 },
    (res) => {
      res.resume(); // drain
      console.log(`Vite dev server ready on port ${PORT}, launching app...`);
      const child = spawn(binaryPath, [], {
        stdio: "inherit",
        shell: false,
      });
      child.on("exit", (code, signal) => {
        process.exit(code ?? (signal ? 1 : 0));
      });
    }
  );

  req.on("error", () => retry());
  req.on("timeout", () => {
    req.destroy();
    retry();
  });
}

function retry() {
  retries++;
  if (retries < MAX_RETRIES) {
    process.stdout.write(".");
    setTimeout(check, RETRY_INTERVAL);
  } else {
    console.error("\nVite dev server did not start within 60s, launching app anyway...");
    const child = spawn(binaryPath, [], { stdio: "inherit", shell: false });
    child.on("exit", (code, signal) => {
      process.exit(code ?? (signal ? 1 : 0));
    });
  }
}

check();
