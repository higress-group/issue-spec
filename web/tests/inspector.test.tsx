import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { ApiProblem } from "../src/lib/api/client";
import { InspectorProvider, ProblemInspector, useInspector } from "../src/app/problem-inspector";

function ConflictTrigger() {
  const inspector = useInspector();
  return <button type="button" onClick={() => inspector.report(new ApiProblem({ status: 409, title: "Conflict", code: "version_conflict", request_id: "request-conflict" }), { display_name: "Local draft" })}>Create conflict</button>;
}

test("version conflicts open the inspector and preserve the local draft", async () => {
  render(<InspectorProvider><ConflictTrigger /><ProblemInspector identity="@alice" permission="admin" /></InspectorProvider>);
  await userEvent.click(screen.getByRole("button", { name: "Create conflict" }));
  expect(screen.getByRole("complementary", { name: "Request inspector" })).toHaveClass("open");
  expect(screen.getByText("request-conflict")).toBeInTheDocument();
  expect(screen.getByText("Your draft is preserved")).toBeInTheDocument();
  expect(screen.getByRole("button", { name: "Copy draft" })).toBeEnabled();
});
