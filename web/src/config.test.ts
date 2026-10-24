import { describe, expect, it } from "vitest";
import { resolveConnectivityApiBase } from "./config";

describe("resolveConnectivityApiBase", () => {
  it("joins the runtime origin with this context's API path", () => {
    expect(resolveConnectivityApiBase({ apiOrigin: "http://localhost:8000" }, false)).toBe(
      "http://localhost:8000/api/vendor-connectivity",
    );
  });

  it("trims trailing slashes off the origin", () => {
    expect(resolveConnectivityApiBase({ apiOrigin: "http://localhost:8000///" }, true)).toBe(
      "http://localhost:8000/api/vendor-connectivity",
    );
  });

  it("falls back to the dev api port outside production", () => {
    expect(resolveConnectivityApiBase({}, false)).toBe("http://localhost:8080");
  });

  it("refuses to fall back in production: a silent fallback is a console talking to nothing", () => {
    expect(() => resolveConnectivityApiBase({}, true)).toThrow(/apiOrigin is required/);
  });
});
