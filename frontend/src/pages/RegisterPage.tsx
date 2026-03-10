import { Link } from 'react-router-dom'
import { ShieldCheck } from 'lucide-react'

export function RegisterPage() {
  return (
    <div className="flex min-h-screen items-center justify-center p-6">
      <div className="w-full max-w-sm space-y-6 text-center">
        <div className="flex justify-center">
          <div className="flex h-14 w-14 items-center justify-center rounded-2xl bg-primary/10">
            <ShieldCheck className="h-7 w-7 text-primary" />
          </div>
        </div>
        <div className="space-y-2">
          <h1 className="text-2xl font-semibold tracking-tight">Invite only</h1>
          <p className="text-sm text-muted-foreground">
            Registration is by invitation only. Ask your administrator to send you an
            invitation link.
          </p>
        </div>
        <Link
          to="/login"
          className="flex h-9 w-full items-center justify-center rounded-md border border-input bg-background px-4 text-sm font-medium shadow-sm transition-colors hover:bg-accent hover:text-accent-foreground"
        >
          Back to sign in
        </Link>
      </div>
    </div>
  )
}

