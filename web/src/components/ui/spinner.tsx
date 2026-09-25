// From coss ui (https://coss.com/ui), MIT licensed; see THIRD_PARTY_NOTICES.
import { Loader2Icon } from "lucide-react";
import type React from "react";
import { cn } from "@/lib/utils";
import { t } from "@/i18n";

export function Spinner({
  className,
  ...props
}: React.ComponentProps<typeof Loader2Icon>): React.ReactElement {
  return (
    <Loader2Icon
      aria-label={t("common.loading")}
      className={cn("animate-spin", className)}
      role="status"
      {...props}
    />
  );
}
