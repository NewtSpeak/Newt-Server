import { Navigate } from "react-router"

import { hasUsableSession } from "~/lib/api"

export default function Home() {
  return <Navigate to={hasUsableSession() ? "/dashboard" : "/login"} replace />
}
