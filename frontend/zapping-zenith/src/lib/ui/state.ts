/** Loading / empty / error states, rendered the same way everywhere. */
export type StateKind = "loading" | "empty" | "error";

export function showState(
  container: HTMLElement,
  kind: StateKind,
  opts: { title?: string; text?: string; action?: { label: string; onClick: () => void } } = {},
) {
  container.replaceChildren();
  const box = document.createElement("div");
  box.className = `state state--${kind}`;
  box.setAttribute("role", kind === "error" ? "alert" : "status");

  const glyph = document.createElement("span");
  glyph.className = "state__glyph";
  glyph.setAttribute("aria-hidden", "true");
  glyph.textContent = kind === "loading" ? "❦" : kind === "error" ? "!" : "❧";
  box.append(glyph);

  if (opts.title) {
    const h = document.createElement("h3");
    h.textContent = opts.title;
    box.append(h);
  }
  if (opts.text) {
    const p = document.createElement("p");
    p.textContent = opts.text;
    box.append(p);
  }
  if (opts.action) {
    const b = document.createElement("button");
    b.type = "button";
    b.className = "btn btn--ghost btn--sm";
    b.textContent = opts.action.label;
    b.addEventListener("click", opts.action.onClick);
    box.append(b);
  }
  container.append(box);
}
