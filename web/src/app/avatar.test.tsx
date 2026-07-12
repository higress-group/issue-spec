import axe from "axe-core";
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it } from "vitest";
import { Avatar, avatarInitials, safeAvatarSource } from "./avatar";

describe("Avatar", () => {
  it("loads only the same-origin stable avatar endpoint with fixed lazy dimensions", async () => {
    const { container } = render(<Avatar login="alice" displayName="Alice Example" src="/api/v1/avatars/alice" size={48} />);
    const image = screen.getByRole("img", { name: "Alice Example avatar" }).querySelector("img");
    expect(image).toHaveAttribute("src", "http://localhost/api/v1/avatars/alice");
    expect(image).toHaveAttribute("loading", "lazy");
    expect(image).toHaveAttribute("width", "48");
    expect(image).toHaveAttribute("height", "48");
    expect((await axe.run(container)).violations).toEqual([]);
  });

  it("rejects malicious sources and falls back when an accepted image is unavailable", () => {
    const { rerender } = render(<Avatar login="alice" displayName="Alice Example" src="https://tracker.example/avatar.png" />);
    expect(screen.getByRole("img", { name: "Alice Example avatar" })).toHaveTextContent("AE");
    expect(screen.queryByRole("img", { hidden: true })?.querySelector("img")).not.toBeInTheDocument();
    rerender(<Avatar login="alice" displayName="Alice Example" src="/api/v1/avatars/alice" />);
    const image = screen.getByRole("img", { name: "Alice Example avatar" }).querySelector("img");
    expect(image).toBeInTheDocument();
    fireEvent.error(image!);
    expect(screen.getByRole("img", { name: "Alice Example avatar" })).toHaveTextContent("AE");
  });

  it("keeps initials deterministic", () => {
    expect(avatarInitials("Alice Example", "alice")).toBe("AE");
    expect(avatarInitials("", "octocat")).toBe("OC");
    expect(safeAvatarSource("javascript:alert(1)")).toBe("");
  });
});
