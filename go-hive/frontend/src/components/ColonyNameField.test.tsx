/**
 * Regression guard for the colony-name field.
 *
 * The shipped input rewrote its value on every keystroke
 * (`value.toLowerCase().replace(/[^a-z0-9_]/g, "")`), which broke Chinese IME
 * input: the field showed the pinyin during composition and went blank the
 * moment the candidate was committed. It also hid capitals/spaces the user had
 * typed. The field must keep the raw text and explain the resulting slug (or
 * why the text cannot be used) underneath; the caller submits the slug.
 */
import { useState } from "react";
import { describe, expect, it } from "vitest";
import { fireEvent, render, screen } from "@testing-library/react";

import ColonyNameField, { colonySlug, isUsableColonySlug } from "./ColonyNameField";

function Harness({ separator = "" as "" | "_" }) {
  const [value, setValue] = useState("");
  return (
    <>
      <ColonyNameField
        value={value}
        onChange={setValue}
        placeholder="e.g. research_team"
        separator={separator}
      />
      <output data-testid="value">{value}</output>
      <output data-testid="slug">{colonySlug(value, separator)}</output>
    </>
  );
}

const input = () => screen.getByPlaceholderText("e.g. research_team") as HTMLInputElement;

describe("colonySlug", () => {
  it("drops rejected characters by default", () => {
    expect(colonySlug("My Colony")).toBe("mycolony");
    expect(colonySlug("Research-Team")).toBe("researchteam");
    expect(colonySlug("a_b_1")).toBe("a_b_1");
  });

  it("folds runs of rejected characters into one underscore when asked", () => {
    expect(colonySlug("My Colony", "_")).toBe("my_colony");
    expect(colonySlug("Research - Team", "_")).toBe("research_team");
  });

  it("cannot make a slug out of Chinese text", () => {
    expect(colonySlug("研究团队")).toBe("");
    expect(colonySlug("研究团队", "_")).toBe("_");
  });

  it("treats separator-only slugs as unusable", () => {
    expect(isUsableColonySlug("")).toBe(false);
    expect(isUsableColonySlug("_")).toBe(false);
    expect(isUsableColonySlug("__")).toBe(false);
    expect(isUsableColonySlug("_team")).toBe(true);
    expect(isUsableColonySlug("research_team")).toBe(true);
  });
});

describe("ColonyNameField", () => {
  it("keeps the committed IME text in the field and flags it instead of blanking it", () => {
    render(<Harness />);
    fireEvent.change(input(), { target: { value: "研究团队" } });

    expect(input().value).toBe("研究团队");
    expect(screen.getByTestId("slug").textContent).toBe("");
    expect(
      screen.getByText(/lowercase letters, digits and underscores only/),
    ).toBeInTheDocument();
    expect(input()).toHaveAttribute("aria-invalid", "true");
  });

  it("keeps the raw text and previews the slug it will create", () => {
    render(<Harness />);
    fireEvent.change(input(), { target: { value: "My Colony" } });

    expect(input().value).toBe("My Colony");
    expect(screen.getByTestId("slug").textContent).toBe("mycolony");
    expect(screen.getByText(/Will be created as/)).toBeInTheDocument();
    expect(input()).toHaveAttribute("aria-invalid", "false");
  });

  it("shows no hint once the text already is a valid slug", () => {
    render(<Harness />);
    fireEvent.change(input(), { target: { value: "research_team" } });

    expect(screen.queryByText(/Will be created as/)).not.toBeInTheDocument();
    expect(
      screen.queryByText(/lowercase letters, digits and underscores only/),
    ).not.toBeInTheDocument();
  });

  it("uses the underscore rule on the sidebar variant", () => {
    render(<Harness separator="_" />);
    fireEvent.change(input(), { target: { value: "Research Team" } });

    expect(input().value).toBe("Research Team");
    expect(screen.getByTestId("slug").textContent).toBe("research_team");
    expect(screen.getByText(/Will be created as/)).toBeInTheDocument();
  });

  it("rejects Chinese under the underscore rule instead of accepting a bare separator", () => {
    render(<Harness separator="_" />);
    fireEvent.change(input(), { target: { value: "研究团队" } });

    expect(input().value).toBe("研究团队");
    expect(screen.getByTestId("slug").textContent).toBe("_");
    expect(
      screen.getByText(/lowercase letters, digits and underscores only/),
    ).toBeInTheDocument();
    expect(input()).toHaveAttribute("aria-invalid", "true");
  });
});
