import { uuid } from "../../src/lib/uuid";

const V4 = /^[0-9a-f]{8}-[0-9a-f]{4}-4[0-9a-f]{3}-[89ab][0-9a-f]{3}-[0-9a-f]{12}$/;

test("uuid v4 even without crypto.randomUUID (http on a LAN is not a secure context)", () => {
  const original = crypto.randomUUID;
  Object.defineProperty(crypto, "randomUUID", { value: undefined, configurable: true });
  try {
    const a = uuid();
    expect(a).toMatch(V4);
    expect(uuid()).not.toBe(a);
  } finally {
    Object.defineProperty(crypto, "randomUUID", { value: original, configurable: true });
  }
  expect(uuid()).toMatch(V4);
});
