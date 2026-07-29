"use client"

import { useEffect, useState } from "react"
import { Navigate } from "react-router"
import { SignupForm } from "~/components/signup-form"
import { api, type RegistrationStatus } from "~/lib/api"

export default function SignupPage() {
  const [open, setOpen] = useState<boolean | null>(null)
  useEffect(() => {
    document.title = "注册 · NewtSpeak"
  }, [])
  useEffect(() => { api<RegistrationStatus>("/auth/registration-status").then(status => setOpen(status.registration_open)).catch(() => setOpen(false)) }, [])
  if (open === false) return <Navigate to="/login" replace />
  if (open === null) return <div className="grid min-h-svh place-items-center text-sm text-muted-foreground">正在检查初始化状态…</div>
  return (
    <div className="flex min-h-svh flex-col items-center justify-center gap-6 bg-muted p-6 md:p-10">
      <div className="flex w-full max-w-sm flex-col gap-6">
        <a href="/" className="flex items-center gap-2 self-center font-medium">
          <img
            src="/logo.png"
            alt="NewtSpeak"
            className="size-8 rounded-md object-cover"
            width={32}
            height={32}
          />
          NewtSpeak
        </a>
        <SignupForm />
      </div>
    </div>
  )
}
