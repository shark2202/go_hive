export function greet(name: string): string {
  console.log(name);
  return name;
}

export function main(): void {
  greet("world");
  console.log("hello", 1);
}
