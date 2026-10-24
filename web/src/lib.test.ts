import { describe, expect, it } from "vitest";
import { idProblem, optionalIdProblem, percent, utcLabel } from "./lib";
import { NO_VALUE } from "@warehouse/ui-kit";

describe("utcLabel", () => {
  it("formats an RFC 3339 instant as 'YYYY-MM-DD HH:MM:SSZ'", () => {
    expect(utcLabel("2026-10-09T08:05:30Z")).toBe("2026-10-09 08:05:30Z");
  });
  it("passes unparseable text through and renders absence as NO_VALUE", () => {
    expect(utcLabel("not-a-date")).toBe("not-a-date");
    expect(utcLabel(undefined)).toBe(NO_VALUE);
  });
});

describe("percent", () => {
  it("renders a rate with one decimal", () => {
    expect(percent(0.97)).toBe("97.0 %");
    expect(percent(0.024)).toBe("2.4 %");
  });
  it("renders an unseen rate as NO_VALUE", () => {
    expect(percent(undefined)).toBe(NO_VALUE);
  });
});

describe("idProblem", () => {
  it("flags an empty id, an over-long id and a malformed id", () => {
    expect(idProblem("vendor id", "")).toMatch(/Enter a vendor id/);
    expect(idProblem("vendor id", "x".repeat(65))).toMatch(/at most 64 characters/);
    expect(idProblem("vendor id", "a/b")).toMatch(/cannot contain spaces/);
  });
  it("accepts a well-formed id", () => {
    expect(idProblem("vendor id", "vend-south-1")).toBeNull();
  });
  it("treats an empty optional filter as fine", () => {
    expect(optionalIdProblem("node id", "")).toBeNull();
    expect(optionalIdProblem("node id", "a b")).toMatch(/cannot contain spaces/);
  });
});
