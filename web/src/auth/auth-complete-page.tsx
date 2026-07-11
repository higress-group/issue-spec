import { useEffect } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { useNavigate } from "react-router-dom";
import { Loading } from "../app/components";
import { queryKeys } from "./session";

export function AuthCompletePage() {
  const client = useQueryClient();
  const navigate = useNavigate();
  useEffect(() => {
    void client.invalidateQueries({ queryKey: queryKeys.context }).then(() => navigate("/", { replace: true }));
  }, [client, navigate]);
  return <Loading label="Opening your workspace" />;
}
