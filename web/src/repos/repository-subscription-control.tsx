import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query";
import { Bell, BellRing, MailWarning } from "lucide-react";
import { Link } from "react-router-dom";
import { useTranslation } from "react-i18next";
import type { z } from "zod";
import { useMeta } from "../auth/session";
import {
  repositoryNotificationApi,
  repositoryNotificationKeys,
  repositorySubscriptionSchema,
} from "./repository-notification-api";

type Subscription = z.infer<typeof repositorySubscriptionSchema>;

export function RepositorySubscriptionControl({ orgId, repoId }: { orgId: string; repoId: string }) {
  const { t } = useTranslation();
  const client = useQueryClient();
  const meta = useMeta();
  const features = meta.data?.features as ({ repository_email_subscriptions?: boolean; email_notifications?: boolean } | undefined);
  const enabled = Boolean(features?.repository_email_subscriptions ?? features?.email_notifications);
  const email = useQuery({
    queryKey: repositoryNotificationKeys.emailStatus,
    queryFn: ({ signal }) => repositoryNotificationApi.emailStatus(signal),
    enabled,
  });
  const subscription = useQuery({
    queryKey: repositoryNotificationKeys.subscription(orgId, repoId),
    queryFn: ({ signal }) => repositoryNotificationApi.subscription(orgId, repoId, signal),
    enabled,
  });
  const mutation = useMutation({
    mutationFn: async (subscribe: boolean) => {
      if (subscribe) return repositoryNotificationApi.subscribe(orgId, repoId);
      await repositoryNotificationApi.unsubscribe(orgId, repoId);
      return { ...(subscription.data as Subscription), subscribed: false, ignored: false, reason: "" };
    },
    onSuccess: (value) => client.setQueryData(repositoryNotificationKeys.subscription(orgId, repoId), value),
  });

  if (!enabled) return null;
  if (email.isLoading || subscription.isLoading) {
    return <button className="button secondary small repository-subscription-control" type="button" disabled>
      <Bell size={15} aria-hidden="true" />{t("repositoryNotifications.loading", { defaultValue: "Loading subscription" })}
    </button>;
  }
  if (!email.data?.notification_email) {
    return <Link className="button secondary small repository-subscription-control" to="/settings/account">
      <MailWarning size={15} aria-hidden="true" />{t("repositoryNotifications.bindEmail", { defaultValue: "Set notification email" })}
    </Link>;
  }
  if (!subscription.data) return null;
  const subscribed = subscription.data.subscribed;
  return <button className="button secondary small repository-subscription-control" type="button"
    aria-pressed={subscribed} disabled={mutation.isPending}
    onClick={() => mutation.mutate(!subscribed)}>
    {subscribed ? <BellRing size={15} aria-hidden="true" /> : <Bell size={15} aria-hidden="true" />}
    {t(subscribed ? "repositoryNotifications.subscribed" : "repositoryNotifications.subscribe", {
      defaultValue: subscribed ? "Subscribed" : "Subscribe",
    })}
  </button>;
}
