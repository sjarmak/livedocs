// Sample TypeScript module for testing.

import { readFileSync } from "fs";

export interface Greeter {
  greet(name: string): string;
}

export class SimpleGreeter implements Greeter {
  greet(name: string): string {
    return `Hello, ${name}!`;
  }
}

export function main(): void {
  const g = new SimpleGreeter();
  console.log(g.greet("world"));
}

type Config = {
  port: number;
  host: string;
};

export enum LogLevel {
  DEBUG,
  INFO,
  WARN,
  ERROR,
}
