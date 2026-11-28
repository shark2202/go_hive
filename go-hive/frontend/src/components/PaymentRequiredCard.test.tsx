import { describe, it, expect } from "vitest";
import { render, screen, fireEvent } from "@testing-library/react";
import PaymentRequiredCard, { parsePaymentRequired } from "./PaymentRequiredCard";

const PAYLOAD = JSON.stringify({
  message: "You exceeded your current quota, please check your plan and billing details.",
  upstream_status: 402,
});

describe("parsePaymentRequired", () => {
  it("reads the event payload's message and upstream status", () => {
    expect(parsePaymentRequired(PAYLOAD)).toEqual({
      detail: "You exceeded your current quota, please check your plan and billing details.",
      upstreamStatus: 402,
    });
  });

  it("tolerates plain text and a missing status", () => {
    expect(parsePaymentRequired("upstream said no")).toEqual({
      detail: "upstream said no",
      upstreamStatus: null,
    });
    expect(parsePaymentRequired('{"message":"no credit"}')).toEqual({
      detail: "no credit",
      upstreamStatus: null,
    });
  });
});

describe("PaymentRequiredCard", () => {
  it("explains the 402, links to the model settings, and folds the raw error away", () => {
    window.location.hash = "";
    render(<PaymentRequiredCard error={parsePaymentRequired(PAYLOAD)} />);

    expect(screen.getByRole("alert")).toBeTruthy();
    expect(screen.getByText("Payment required.")).toBeTruthy();
    expect(screen.getByText("HTTP 402")).toBeTruthy();
    expect(screen.queryByText(/exceeded your current quota/)).toBeNull();

    fireEvent.click(screen.getByRole("button", { name: /details/i }));
    expect(screen.getByText(/exceeded your current quota/)).toBeTruthy();

    fireEvent.click(screen.getByRole("button", { name: /model settings/i }));
    expect(window.location.hash).toBe("#/models");
  });
});
