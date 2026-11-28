import { describe, it, expect } from "vitest";
import { slugToColonyId } from "./colony-registry";

describe("slugToColonyId injectivity", () => {
  it("collapses distinct backend-legal slugs onto one route id", () => {
    // Every one of these is accepted by the backend colony-name gate
    // (^[a-z0-9_]+$) and can exist on disk simultaneously.
    const legal = ["x", "email_x", "x_agent", "email_x_agent"];
    for (const s of legal) expect(s).toMatch(/^[a-z0-9_]+$/);

    const ids = legal.map(slugToColonyId);
    expect(ids).toEqual(["x", "x", "x", "x"]);
    expect(new Set(ids).size).toBe(1); // 4 colonies -> 1 route id
  });

  it("realistic pairs that collide", () => {
    expect(slugToColonyId("email_newsletter_agent"))
      .toBe(slugToColonyId("newsletter_agent"));   // both -> "newsletter"
    expect(slugToColonyId("sdr_agent")).toBe(slugToColonyId("email_sdr_agent"));
    expect(slugToColonyId("email_inbox_management"))
      .toBe(slugToColonyId("inbox_management"));
  });

  it("is NOT injective in general", () => {
    const slugs = ["x","email_x","x_agent","email_x_agent",
                   "sdr","email_sdr","sdr_agent","email_sdr_agent"];
    expect(new Set(slugs.map(slugToColonyId)).size)
      .toBeLessThan(slugs.length);
  });
});
