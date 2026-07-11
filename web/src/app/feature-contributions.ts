import type { ComponentType } from "react";
import type { RouteObject } from "react-router-dom";

export type FeatureNavItem = {
  label: string;
  to: string;
  capability?: string;
  requiredAction?: string;
};

export type FeatureContribution = {
  routes?: RouteObject[];
  navigation?: FeatureNavItem[];
  inspector?: ComponentType;
};

type ContributionModule = { default: FeatureContribution };

const discovered = import.meta.glob<ContributionModule>("../features/*/contribution.tsx", { eager: true });

export const featureContributions = Object.entries(discovered)
  .sort(([left], [right]) => left.localeCompare(right))
  .map(([, module]) => module.default);

export const featureRoutes = featureContributions.flatMap((feature) => feature.routes ?? []);
export const featureNavigation = featureContributions.flatMap((feature) => feature.navigation ?? []);
