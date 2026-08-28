import { faPalette } from "@fortawesome/free-solid-svg-icons";
import React from "react";
import { Dropdown, DropdownButton } from "react-bootstrap";
import { useIntl } from "react-intl";
import {
  normalizePerformerCardStyle,
  performerCardStyles,
  PerformerCardStyle,
} from "src/core/config";
import { useConfigureUISetting } from "src/core/StashService";
import { useConfigurationContext } from "src/hooks/Config";
import { useToast } from "src/hooks/Toast";
import { Icon } from "../Shared/Icon";

const localePrefix = "config.ui.performer_list.options.card_style";

export const PerformerCardStyleSelector: React.FC = () => {
  const intl = useIntl();
  const Toast = useToast();
  const { configuration } = useConfigurationContext();
  const [saveUISetting, { loading }] = useConfigureUISetting();
  const currentStyle = normalizePerformerCardStyle(
    configuration.ui.performerCardStyle
  );
  const currentName = intl.formatMessage({
    id: `${localePrefix}.${currentStyle}`,
  });
  const currentLabel = intl.formatMessage(
    { id: `${localePrefix}.label_current` },
    { current: currentName }
  );

  async function selectStyle(nextStyle: PerformerCardStyle) {
    if (nextStyle === currentStyle) return;

    try {
      await saveUISetting({
        variables: {
          key: "performerCardStyle",
          value: nextStyle,
        },
      });
    } catch (error) {
      Toast.error(error);
    }
  }

  return (
    <DropdownButton
      id="performer-card-style-selector"
      className="performer-card-style-selector mr-2"
      size="sm"
      variant="secondary"
      disabled={loading}
      title={
        <>
          <Icon icon={faPalette} />
          <span className="sr-only">{currentLabel}</span>
        </>
      }
    >
      <Dropdown.Header>
        {intl.formatMessage({ id: `${localePrefix}.heading` })}
      </Dropdown.Header>
      {performerCardStyles.map((style) => (
        <Dropdown.Item
          key={style}
          active={style === currentStyle}
          onClick={() => void selectStyle(style)}
        >
          {intl.formatMessage({ id: `${localePrefix}.${style}` })}
        </Dropdown.Item>
      ))}
    </DropdownButton>
  );
};
