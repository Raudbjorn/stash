import React, { useCallback, useEffect, useMemo, useState } from "react";
import { FormattedMessage } from "react-intl";
import { Dropdown, Nav, Tab } from "react-bootstrap";
import { useHistory } from "react-router-dom";
import { faCog } from "@fortawesome/free-solid-svg-icons";
import { Counter } from "../Counter";
import { Icon } from "../Icon";
import { PatchComponent } from "src/patch";

export const TabTitleCounter: React.FC<{
  messageID: string;
  count: number;
  abbreviateCounter: boolean;
}> = PatchComponent(
  "TabTitleCounter",
  ({ messageID, count, abbreviateCounter }) => {
    return (
      <>
        <FormattedMessage id={messageID} />
        <Counter count={count} abbreviateCounter={abbreviateCounter} hideZero />
      </>
    );
  }
);

function deriveStorageKey(baseURL: string): string {
  const segment = baseURL.split("/").filter(Boolean)[0];
  return `defaultTab:${segment}`;
}

function getStoredDefault(
  storageKey: string,
  validTabs: readonly string[],
  fallback: string
): string {
  const stored = localStorage.getItem(storageKey);
  if (stored && (validTabs as readonly string[]).includes(stored)) {
    return stored;
  }
  return fallback;
}

interface StashTabsProps {
  activeKey: string | undefined;
  onSelect: (key: string | null) => void;
  validTabs: readonly string[];
  defaultTabKey: string;
  setDefaultTabKey: (tab: string) => void;
}

export function useTabKey(props: {
  tabKey: string | undefined;
  validTabs: readonly string[];
  defaultTabKey: string;
  baseURL: string;
}) {
  const { tabKey, validTabs, defaultTabKey, baseURL } = props;

  const storageKey = deriveStorageKey(baseURL);
  const [currentDefault, setCurrentDefault] = useState(() =>
    getStoredDefault(storageKey, validTabs, defaultTabKey)
  );

  const history = useHistory();
  const activeTabKey =
    tabKey && tabKey !== "default" && validTabs.includes(tabKey)
      ? tabKey
      : currentDefault;

  const setTabKey = useCallback(
    (newTabKey: string | null) => {
      if (
        !newTabKey ||
        newTabKey === "default" ||
        !validTabs.includes(newTabKey)
      ) {
        newTabKey = currentDefault;
      }
      if (newTabKey === activeTabKey) return;

      history.replace(`${baseURL}/${newTabKey}`);
    },
    [activeTabKey, currentDefault, validTabs, history, baseURL]
  );

  const setDefaultTabKey = useCallback(
    (tab: string) => {
      if ((validTabs as readonly string[]).includes(tab)) {
        localStorage.setItem(storageKey, tab);
        setCurrentDefault(tab);
      }
    },
    [storageKey, validTabs]
  );

  useEffect(() => {
    if (tabKey !== activeTabKey) {
      history.replace(`${baseURL}/${activeTabKey}`);
    }
  }, [activeTabKey, baseURL, history, tabKey]);

  const tabsProps: StashTabsProps = useMemo(
    () => ({
      activeKey: activeTabKey,
      onSelect: setTabKey,
      validTabs,
      defaultTabKey: currentDefault,
      setDefaultTabKey,
    }),
    [activeTabKey, setTabKey, validTabs, currentDefault, setDefaultTabKey]
  );

  return { activeTabKey, setTabKey, tabsProps };
}

const DefaultTabDropdown: React.FC<{
  validTabs: readonly string[];
  currentDefault: string;
  onSetDefault: (tab: string) => void;
}> = ({ validTabs, currentDefault, onSetDefault }) => (
  <Dropdown className="default-tab-dropdown">
    <Dropdown.Toggle variant="link" size="sm" id="default-tab-settings">
      <Icon icon={faCog} />
    </Dropdown.Toggle>
    <Dropdown.Menu>
      <Dropdown.Header>
        <FormattedMessage id="default_tab" defaultMessage="Default Tab" />
      </Dropdown.Header>
      {validTabs.map((tab) => (
        <Dropdown.Item
          key={tab}
          active={currentDefault === tab}
          onClick={() => onSetDefault(tab)}
        >
          <FormattedMessage id={tab} />
        </Dropdown.Item>
      ))}
    </Dropdown.Menu>
  </Dropdown>
);

export const StashTabs: React.FC<
  StashTabsProps & {
    id: string;
    mountOnEnter?: boolean;
    unmountOnExit?: boolean;
    children: React.ReactNode;
  }
> = ({
  id,
  activeKey,
  onSelect,
  validTabs,
  defaultTabKey,
  setDefaultTabKey,
  mountOnEnter,
  unmountOnExit,
  children,
}) => {
  const tabs = React.Children.toArray(children).filter(
    (child): child is React.ReactElement =>
      React.isValidElement(child) && child.props.eventKey != null
  );

  return (
    <Tab.Container id={id} activeKey={activeKey} onSelect={onSelect}>
      <Nav as="nav" variant="tabs">
        {tabs.map((tab) => (
          <Nav.Item key={tab.props.eventKey}>
            <Nav.Link
              eventKey={tab.props.eventKey}
              disabled={tab.props.disabled}
            >
              {tab.props.title}
            </Nav.Link>
          </Nav.Item>
        ))}
        <DefaultTabDropdown
          validTabs={validTabs}
          currentDefault={defaultTabKey}
          onSetDefault={setDefaultTabKey}
        />
      </Nav>
      <Tab.Content>
        {tabs.map((tab) => (
          <Tab.Pane
            key={tab.props.eventKey}
            eventKey={tab.props.eventKey}
            mountOnEnter={mountOnEnter}
            unmountOnExit={unmountOnExit}
          >
            {tab.props.children}
          </Tab.Pane>
        ))}
      </Tab.Content>
    </Tab.Container>
  );
};
