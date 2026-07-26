import { Link, useLocation } from "react-router"
import { AudioLinesIcon } from "lucide-react"

import { NavUser } from "~/components/nav-user"
import {
  Sidebar,
  SidebarContent,
  SidebarFooter,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarHeader,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
} from "~/components/ui/sidebar"
import type { User } from "~/lib/api"
import { NAV_GROUPS } from "~/lib/nav"

export function AppSidebar({
  user,
  onLogout,
  ...props
}: React.ComponentProps<typeof Sidebar> & { user: User; onLogout: () => void }) {
  const location = useLocation()

  return (
    <Sidebar collapsible="offcanvas" {...props}>
      <SidebarHeader>
        <SidebarMenu>
          <SidebarMenuItem>
            <SidebarMenuButton className="data-[slot=sidebar-menu-button]:p-1.5!" render={<Link to="/dashboard" />}>
              <span className="grid size-6 place-items-center rounded-lg bg-primary text-primary-foreground">
                <AudioLinesIcon className="size-4!" />
              </span>
              <span className="text-base font-semibold">OwlSpeak</span>
              <span className="ml-auto rounded-full border px-1.5 py-px text-[10px] leading-4 text-muted-foreground">管理台</span>
            </SidebarMenuButton>
          </SidebarMenuItem>
        </SidebarMenu>
      </SidebarHeader>
      <SidebarContent>
        {NAV_GROUPS.map(group => (
          <SidebarGroup key={group.label} className="py-1">
            <SidebarGroupLabel>{group.label}</SidebarGroupLabel>
            <SidebarGroupContent>
              <SidebarMenu>
                {group.items.map(item => {
                  const active = location.pathname === item.url || location.pathname.startsWith(`${item.url}/`)
                  return (
                    <SidebarMenuItem key={item.url}>
                      <SidebarMenuButton tooltip={item.title} isActive={active} render={<Link to={item.url} />}>
                        <item.icon />
                        <span>{item.title}</span>
                      </SidebarMenuButton>
                    </SidebarMenuItem>
                  )
                })}
              </SidebarMenu>
            </SidebarGroupContent>
          </SidebarGroup>
        ))}
      </SidebarContent>
      <SidebarFooter>
        <NavUser
          user={{ id: user.id, name: user.username, email: user.email, avatar: user.avatar_url ?? "" }}
          onLogout={onLogout}
        />
      </SidebarFooter>
    </Sidebar>
  )
}
