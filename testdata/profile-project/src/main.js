import { answer } from './util.js';
class Counter {
  #value = answer;
  next() { return this.#value + 1; }
}
export const result = new Counter().next();
