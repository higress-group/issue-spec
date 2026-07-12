import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { Loading } from "../app/components";
import { queryKeys } from "./session";
import { api } from "../lib/api/resources";

export function AuthCompletePage() {
  const client = useQueryClient();
  const navigate = useNavigate();
  useEffect(() => {
    void client.fetchQuery({ queryKey: queryKeys.context, queryFn: ({ signal }) => api.context(signal) }).then(() => navigate("/", { replace: true }));
  }, [client, navigate]);
  return <Loading label="Opening your workspace" />;
}
