import { authApi } from "../api/auth";
import { describeError } from "../api/client";

function wire(form: HTMLFormElement, run: (data: FormData) => Promise<void>) {
  const errorEl = form.querySelector<HTMLElement>("[data-form-error]")!;
  const submit = form.querySelector<HTMLButtonElement>("button[type=submit]")!;
  form.addEventListener("submit", async (e) => {
    e.preventDefault();
    errorEl.hidden = true;
    if (!form.checkValidity()) { form.reportValidity(); return; }
    submit.disabled = true;
    try {
      await run(new FormData(form));
    } catch (err) {
      errorEl.textContent = describeError(err);
      errorEl.hidden = false;
    } finally {
      submit.disabled = false;
    }
  });
}

export function wireLogin(form: HTMLFormElement) {
  wire(form, async (d) => {
    await authApi.login(String(d.get("email")).trim(), String(d.get("password")));
    window.location.href = "/dashboard";
  });
}

export function wireRegister(form: HTMLFormElement, onVerify: () => void) {
  wire(form, async (d) => {
    const res = await authApi.register(
      String(d.get("displayName")).trim(),
      String(d.get("email")).trim(),
      String(d.get("password")),
    );
    if (res && res.verificationRequired === true) { onVerify(); return; }
    window.location.href = "/dashboard";
  });
}
