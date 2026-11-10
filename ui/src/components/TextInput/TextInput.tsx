import "./TextInput.css";

export type TextInputProps = {
  id: string;
  label: string;
  placeholder?: string;
  value: string;
  onChange: (value: string) => void;
  disabled?: boolean;
  invalid?: boolean;
  message?: string;
  /** A request triggered by this input is in flight (12px spinner at the right edge, §6A.4). */
  pending?: boolean;
  /** The label is for screen readers only, and the input fills its container (the drawer's field filter). */
  hideLabel?: boolean;
  "data-force"?: "focus";
};

/** A filter input (handoff §6.1): label in micro caps to the left; free text or field=value. */
export function TextInput({ id, label, placeholder, value, onChange, disabled, invalid, message, pending, hideLabel, ...rest }: TextInputProps) {
  const msgId = message ? `${id}-message` : undefined;
  const field = (
    <div className={`flex items-center gap-2 ${hideLabel ? "w-full" : ""}`}>
      <label htmlFor={id} className={hideLabel ? "sr-only" : "text-input-label"}>
        {label}
      </label>
      <span className={`relative flex ${hideLabel ? "flex-1" : ""}`}>
        <input
          id={id}
          type="search"
          value={value}
          placeholder={placeholder}
          disabled={disabled}
          aria-invalid={invalid || undefined}
          aria-describedby={msgId}
          data-force={rest["data-force"]}
          onChange={(e) => onChange(e.target.value)}
          className={`text-input rounded-sm ${pending ? "text-input-pending" : ""} ${hideLabel ? "w-full" : ""}`}
        />
        {pending && <span aria-hidden className="btn-spinner text-input-spinner" />}
      </span>
    </div>
  );
  if (!message) return field;
  return (
    <div className="flex flex-col gap-1">
      {field}
      <span id={msgId} className={`text-input-message ${invalid ? "text-ink" : "text-ink-muted"}`}>
        {invalid && "✕ "}
        {message}
      </span>
    </div>
  );
}
