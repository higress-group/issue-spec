import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";
import { renderApp } from "../../tests/render";
import { SecretDialog } from "./components";

describe("SecretDialog clipboard compatibility", () => {
  it("falls back when Clipboard API writes fail and reports success", async () => {
    const user = userEvent.setup();
    const clipboardDescriptor = Object.getOwnPropertyDescriptor(navigator, "clipboard");
    const execCommandDescriptor = Object.getOwnPropertyDescriptor(document, "execCommand");
    const writeText = vi.fn().mockRejectedValue(new Error("insecure context"));
    let copiedValue = "";
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText } });
    Object.defineProperty(document, "execCommand", { configurable: true, value: vi.fn(() => {
      copiedValue = document.querySelector<HTMLTextAreaElement>("textarea[readonly]")?.value ?? "";
      return true;
    }) });
    try {
      renderApp(<SecretDialog secret="iss_pat_secret" title="Save this access token" onClose={vi.fn()} />);
      await user.click(screen.getByRole("button", { name: "Copy credential" }));
      expect(writeText).toHaveBeenCalledWith("iss_pat_secret");
      expect(copiedValue).toBe("iss_pat_secret");
      expect(screen.getByRole("button", { name: "Credential copied" })).toBeVisible();
    } finally {
      restoreProperty(navigator, "clipboard", clipboardDescriptor);
      restoreProperty(document, "execCommand", execCommandDescriptor);
    }
  });

  it("keeps the shown-once credential selectable and reports total copy failure", async () => {
    const user = userEvent.setup();
    const clipboardDescriptor = Object.getOwnPropertyDescriptor(navigator, "clipboard");
    const execCommandDescriptor = Object.getOwnPropertyDescriptor(document, "execCommand");
    Object.defineProperty(navigator, "clipboard", { configurable: true, value: { writeText: vi.fn().mockRejectedValue(new Error("denied")) } });
    Object.defineProperty(document, "execCommand", { configurable: true, value: vi.fn(() => false) });
    try {
      renderApp(<SecretDialog secret="iss_pat_secret" title="Save this access token" onClose={vi.fn()} />);
      await user.click(screen.getByRole("button", { name: "Copy credential" }));
      expect(screen.getByRole("button", { name: "Copy failed — select it manually" })).toBeVisible();
      expect(screen.getByText("iss_pat_secret")).toHaveAttribute("tabindex", "0");
    } finally {
      restoreProperty(navigator, "clipboard", clipboardDescriptor);
      restoreProperty(document, "execCommand", execCommandDescriptor);
    }
  });
});

function restoreProperty(target: object, key: PropertyKey, descriptor: PropertyDescriptor | undefined) {
  if (descriptor) Object.defineProperty(target, key, descriptor);
  else Reflect.deleteProperty(target, key);
}
