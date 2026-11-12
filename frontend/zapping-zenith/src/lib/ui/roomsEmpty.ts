/** First-room empty state. Safe DOM only. */
function t<K extends keyof HTMLElementTagNameMap>(tag: K, cls: string, text?: string) {
  const e = document.createElement(tag);
  e.className = cls;
  if (text !== undefined) e.textContent = text;
  return e;
}

export function roomsEmpty(opts: { canCreate: boolean; onCreate: () => void }): HTMLElement {
  const box = t("div", "gx-empty");
  box.setAttribute("role", "status");

  const seal = t("span", "gx-empty__seal", "❦");
  seal.setAttribute("aria-hidden", "true");

  box.append(
    seal,
    t("h3", "gx-empty__title", "Your group has no study rooms yet"),
    t(
      "p",
      "gx-empty__lead",
      // "A study room is where the group actually studies together: a shared space members step into to read,",
    ),
  );

  const kinds = t("div", "gx-empty__kinds");
  const kind = (title: string, text: string) => {
    const d = t("div", "gx-empty__kind");
    d.append(t("strong", "", title), t("span", "", text));
    return d;
  };
  kinds.append(
    kind("Open to the group", "Every member can walk in. Best for your main study space."),
    kind("Restricted", "Only people a room host lets in. Good for mentors or a small circle."),
  );
  box.append(kinds);

  if (opts.canCreate) {
    const b = t("button", "btn btn--primary gx-empty__cta", "Create your first room");
    b.type = "button";
    b.addEventListener("click", opts.onCreate);
    box.append(b, t("p", "gx-empty__hint", "It takes a few seconds. You can add more rooms any time."));
  } else {
    box.append(t("p", "gx-empty__hint", "Only moderators, admins and owners can create rooms. Ask one of them to set up the first room, and it will appear here."));
  }
  return box;
}
