export const GOOGLE_ISSUER = "https://accounts.google.com";

export type VerifiedIdentity = {
  email: string;
  name?: string;
  issuer: string;
  subject: string;
  emailAuthoritative: boolean;
};

export function validSubject(subject: unknown): subject is string {
  return typeof subject === "string" && /^[\x21-\x7e]{1,1024}$/.test(subject);
}

export function googleEmailAuthoritative(email: string, hd: unknown): boolean {
  const separator = email.lastIndexOf("@");
  const domain = separator > 0 ? email.slice(separator + 1) : "";
  const hostedDomain = typeof hd === "string" ? hd.trim().toLowerCase() : "";
  return email.endsWith("@gmail.com") || (hostedDomain !== "" && domain === hostedDomain);
}
