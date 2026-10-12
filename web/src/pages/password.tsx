import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { json, message, requestVoid } from "../api";
import { Alert, Field } from "../components";
import { useSession } from "../session";
import { t } from "../i18n";

// PasswordForm changes the signed-in account's password and signs out.
export function PasswordForm() {
  const { clearLocal } = useSession();
  const nav = useNavigate();
  const [error, setError] = useState(""),
    [busy, setBusy] = useState(false);
  async function submit(e: FormEvent<HTMLFormElement>) {
    e.preventDefault();
    const f = new FormData(e.currentTarget);
    if (f.get("new_password") !== f.get("confirm")) {
      setError(t("两次输入的新密码不一致。"));
      return;
    }
    setBusy(true);
    setError("");
    try {
      await requestVoid(
        "/me/password",
        json("POST", {
          current_password: f.get("current_password"),
          new_password: f.get("new_password"),
        }),
      );
      clearLocal();
      nav("/login", {
        replace: true,
        state: { notice: t("密码已修改，请重新登录") },
      });
    } catch (e) {
      setError(message(e));
      setBusy(false);
    }
  }
  return (
    <form onSubmit={submit}>
      <Alert error={error} />
      <Field label={t("当前密码")}>
        <input
          type="password"
          name="current_password"
          autoComplete="current-password"
          required
        />
      </Field>
      <Field label={t("新密码")} hint={t("至少 12 个字符")}>
        <input
          type="password"
          name="new_password"
          autoComplete="new-password"
          required
          minLength={12}
          maxLength={1024}
        />
      </Field>
      <Field label={t("确认新密码")}>
        <input
          type="password"
          name="confirm"
          autoComplete="new-password"
          required
          minLength={12}
          maxLength={1024}
        />
      </Field>
      <button className="primary" disabled={busy}>
        {busy ? t("正在修改…") : t("修改密码")}
      </button>
    </form>
  );
}
