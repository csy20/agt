const test = require("node:test");
const assert = require("node:assert/strict");
const { quote } = require("./quote.js");

test("escapes embedded quotes", () => {
  assert.equal(quote('say "hi"'), '"say \\"hi\\""');
});
