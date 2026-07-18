import { act, render, screen } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import i18n from "../../i18n/i18n";
import { formatPreciseTime, formatRelativeTime, isLaterTimestamp, PreciseRelativeTime, useSecondClock } from "./relative-time";

const now = Date.parse("2026-07-18T12:00:00Z");
const before = (milliseconds: number) => new Date(now - milliseconds).toISOString();
const after = (milliseconds: number) => new Date(now + milliseconds).toISOString();

function ClockProbe({ value }: { value: string }) {
  return <PreciseRelativeTime value={value} now={useSecondClock()} />;
}

afterEach(() => vi.useRealTimers());

describe("precise relative issue timestamps", () => {
  it("formats compact past and future values across every supported unit", () => {
    expect(formatRelativeTime(before(30_000), now, "en")).toBe("30s ago");
    expect(formatRelativeTime(before(8 * 60_000), now, "en")).toBe("8m ago");
    expect(formatRelativeTime(before(3 * 60 * 60_000), now, "en")).toBe("3h ago");
    expect(formatRelativeTime(before(5 * 24 * 60 * 60_000), now, "en")).toBe("5d ago");
    expect(formatRelativeTime(before(60 * 24 * 60 * 60_000), now, "en")).toBe("2mo ago");
    expect(formatRelativeTime(before(2 * 365 * 24 * 60 * 60_000), now, "en")).toBe("2y ago");
    expect(formatRelativeTime(after(8 * 60_000), now, "en")).toBe("in 8m");
    expect(formatRelativeTime(before(8 * 60_000), now, "zh-CN")).toBe("8分钟前");
    expect(formatRelativeTime(after(8 * 60_000), now, "zh-CN")).toBe("8分钟后");
  });

  it("returns no invented relative or precise value for invalid timestamps", () => {
    expect(formatRelativeTime("not-a-time", now, "en")).toBeNull();
    expect(formatPreciseTime("not-a-time", "en")).toBeNull();
    expect(isLaterTimestamp("not-a-time", before(1_000))).toBe(false);
    expect(isLaterTimestamp(after(1_000), "not-a-time")).toBe(false);
  });

  it("renders machine-readable, second-precise and timezone-aware time semantics", () => {
    const value = before(8 * 60_000);
    const { container } = render(<PreciseRelativeTime value={value} now={now} />);
    const time = container.querySelector("time");
    expect(time).toHaveTextContent("8m ago");
    expect(time).toHaveAttribute("datetime", value);
    expect(time?.title).toMatch(/:\d{2}:\d{2}/);
    expect(time?.title).toMatch(/GMT|UTC/);
    expect(time?.getAttribute("aria-label")).toContain(time?.title ?? "missing-title");
  });

  it("uses localized Chinese relative text and a safe non-time fallback", async () => {
    await i18n.changeLanguage("zh-CN");
    const valid = render(<PreciseRelativeTime value={before(8 * 60_000)} now={now} />);
    expect(valid.getByText("8分钟前")).toBeInstanceOf(HTMLTimeElement);
    valid.unmount();
    const invalid = render(<PreciseRelativeTime value="bad timestamp" now={now} />);
    expect(invalid.getByText("bad timestamp")).toBeInstanceOf(HTMLSpanElement);
    expect(invalid.container.querySelector("time")).toBeNull();
  });

  it("creates one one-second timer and advances all formatting from its shared value", () => {
    vi.useFakeTimers();
    vi.setSystemTime(now);
    const setInterval = vi.spyOn(window, "setInterval");
    const view = render(<ClockProbe value={before(1_000)} />);
    try {
      expect(setInterval).toHaveBeenCalledTimes(1);
      expect(setInterval).toHaveBeenCalledWith(expect.any(Function), 1_000);
      expect(screen.getByText("1s ago")).toBeVisible();
      act(() => vi.advanceTimersByTime(1_000));
      expect(screen.getByText("2s ago")).toBeVisible();
    } finally {
      view.unmount();
    }
  });
});
