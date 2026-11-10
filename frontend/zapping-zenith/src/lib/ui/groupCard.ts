import type { Group } from "../api/groups";

function t(tag: string, cls: string, text?: string) {
  const e = document.createElement(tag);
  e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

/** Builds a group card with textContent only (never innerHTML) so group text can't inject markup. */
export function groupCard(g: Group, forceMember = false): HTMLElement {
  const name = g.name ?? "Untitled group";
  const membership = forceMember ? "member" : g.membership ?? "none";

  const card = t("article", "gcard");
  const tone = [...name].reduce((n, c) => n + c.charCodeAt(0), 0) % 5;
  const cover = t("div", `gcard__cover tone-${tone}`);
  cover.setAttribute("aria-hidden", "true");
  cover.append(t("span", "gcard__mono", name.replace(/^(the|a|an)\s+/i, "").charAt(0).toUpperCase()));

  const body = t("div", "gcard__body");
  body.append(t("p", "gcard__kicker", g.visibility === "private" ? "Private group" : "Public group"));
  body.append(t("h3", "gcard__title", name));
  if (g.description) body.append(t("p", "gcard__desc", g.description));

  const meta = t("p", "gcard__meta");
  const bits: string[] = [];
  if (typeof g.memberCount === "number") bits.push(`${g.memberCount} ${g.memberCount === 1 ? "member" : "members"}`);
  if (membership === "member" && g.role) bits.push(g.role.charAt(0).toUpperCase() + g.role.slice(1));
  meta.textContent = bits.join(" · ");
  if (bits.length) body.append(meta);

  const actions = t("div", "gcard__actions");
  if (membership === "member") {
    const a = t("a", "btn btn--primary btn--sm", "View group") as HTMLAnchorElement;
    a.href = `/groups/${encodeURIComponent(g.id)}`;
    actions.append(a);
  } else if (membership === "pending") {
    actions.append(t("span", "gcard__badge", "Request pending"));
  } else if (membership === "invited") {
    const b = t("button", "btn btn--secondary btn--sm", "Accept invitation") as HTMLButtonElement;
    b.type = "button";
    b.dataset.joinId = g.id;
    actions.append(b);
  } else {
    const b = t("button", "btn btn--secondary btn--sm", "Join group") as HTMLButtonElement;
    b.type = "button";
    b.dataset.joinId = g.id;
    actions.append(b);
  }
  body.append(actions);

  card.append(cover, body);
  return card;
}
